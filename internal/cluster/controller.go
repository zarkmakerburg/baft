package cluster

import (
    "context"
    "errors"
    "fmt"
    "sort"
    "sync"
    "sync/atomic"
    "time"

    "github.com/zarkmakerburg/baft/internal/clustersync"
    "github.com/zarkmakerburg/baft/internal/config"
    "github.com/zarkmakerburg/baft/internal/node"
)

var (
    ErrInPlaceNodeMutationUnsupported = errors.New("in-place node mutation is unsupported")
    ErrWorkerControllerClosed = errors.New("worker controller is closed")
)

const runtimeStartupTimeout = 10 * time.Second

type managedRuntimeRunner interface {
    Run(context.Context, config.Config) error
    DialerReady() <-chan struct{}
    DialerReadiness() error
}

type RuntimeFailure struct {
    NodeID string
    Instance uint64
    Err error
}

const (
    TopologyAfterPrepare = "TOPOLOGY_AFTER_PREPARE"
    TopologyAfterStageAdd = "TOPOLOGY_AFTER_STAGE_ADD"
    TopologyAfterStopRemove = "TOPOLOGY_AFTER_STOP_REMOVE"
    TopologyBeforeCommit = "TOPOLOGY_BEFORE_COMMIT"
    TopologyAfterCommit = "TOPOLOGY_AFTER_COMMIT"
)

type managedRuntime struct {
    nodeID string
    managed clustersync.ManagedNodeConfig
    runner managedRuntimeRunner
    cancel context.CancelFunc
    done chan struct{}
    instance uint64
    planned atomic.Bool
    stopOnce sync.Once
    errMu sync.Mutex
    runErr error
}

func (m *managedRuntime) setErr(err error){m.errMu.Lock();m.runErr=err;m.errMu.Unlock()}
func (m *managedRuntime) err() error {m.errMu.Lock();defer m.errMu.Unlock();return m.runErr}

type WorkerController struct {
    mu sync.Mutex
    rootCtx context.Context
    rootCancel context.CancelFunc
    engine *clustersync.Engine
    template clustersync.WorkerTemplate
    runtimes map[string]*managedRuntime
    lastStopped map[string]*managedRuntime
    failures chan RuntimeFailure
    newRuntime func() managedRuntimeRunner
    nextInstance uint64
    topologyHook func(string,uint64)
    closed bool
}

func NewWorkerController(parent context.Context, engine *clustersync.Engine, template clustersync.WorkerTemplate) (*WorkerController,error) {
    if engine==nil{return nil,errors.New("sync engine is required")}
    if parent==nil{parent=context.Background()}
    ctx,cancel:=context.WithCancel(parent)
    return &WorkerController{
        rootCtx:ctx,rootCancel:cancel,engine:engine,template:template,
        runtimes:map[string]*managedRuntime{},lastStopped:map[string]*managedRuntime{},
        failures:make(chan RuntimeFailure,64),
        newRuntime:func() managedRuntimeRunner{return node.NewRuntime()},
    },nil
}

func (c *WorkerController) ApplyToken(token string, now time.Time) (clustersync.Snapshot,bool,error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.closed{return clustersync.Snapshot{},false,ErrWorkerControllerClosed}

    prepared,err:=c.engine.PrepareToken(token,now)
    if err!=nil{return clustersync.Snapshot{},false,err}
    if !prepared.Changed(){return prepared.Snapshot(),false,nil}

    diff:=prepared.Diff()
    c.fireTopologyHookForTest(TopologyAfterPrepare,prepared.Snapshot().Generation)
    if len(diff.ChangedNodeIDs)>0{
        return clustersync.Snapshot{},false,fmt.Errorf("%w: %v",ErrInPlaceNodeMutationUnsupported,diff.ChangedNodeIDs)
    }
    targetList,err:=clustersync.BuildManagedWorkerConfigs(prepared.Snapshot(),c.template)
    if err!=nil{return clustersync.Snapshot{},false,err}
    target:=make(map[string]clustersync.ManagedNodeConfig,len(targetList))
    for _,mc:=range targetList{target[mc.NodeID]=mc}

    for id,current:=range c.runtimes{
        if next,ok:=target[id];ok && current.managed.Digest!=next.Digest{
            return clustersync.Snapshot{},false,fmt.Errorf("%w: NodeID %s config digest changed",ErrInPlaceNodeMutationUnsupported,id)
        }
    }

    added:=append([]string(nil),diff.AddedNodeIDs...)
    removed:=append([]string(nil),diff.RemovedNodeIDs...)
    sort.Strings(added);sort.Strings(removed)

    staged:=make(map[string]*managedRuntime,len(added))
    for _,id:=range added{
        mc,ok:=target[id];if !ok{c.stopMany(staged);return clustersync.Snapshot{},false,fmt.Errorf("target config missing NodeID %s",id)}
        mr,err:=c.startManaged(mc)
        if err!=nil{
            c.stopMany(staged)
            return clustersync.Snapshot{},false,fmt.Errorf("start NodeID %s: %w",id,err)
        }
        staged[id]=mr
    }

    if len(added)>0 { c.fireTopologyHookForTest(TopologyAfterStageAdd,prepared.Snapshot().Generation) }

    stopped:=make([]*managedRuntime,0,len(removed))
    for _,id:=range removed{
        mr:=c.runtimes[id]
        if mr==nil{c.stopMany(staged);return clustersync.Snapshot{},false,fmt.Errorf("committed runtime missing NodeID %s",id)}
        if err:=c.stopManaged(mr);err!=nil{
            c.rollbackStopped(stopped)
            c.stopMany(staged)
            return clustersync.Snapshot{},false,fmt.Errorf("stop NodeID %s: %w",id,err)
        }
        stopped=append(stopped,mr)
    }
    if len(removed)>0 { c.fireTopologyHookForTest(TopologyAfterStopRemove,prepared.Snapshot().Generation) }

    // Final pre-commit liveness fence: a runtime that passed readiness but
    // exited before topology publication must not produce control/runtime split-brain.
    checkRunning:=func(id string,mr *managedRuntime) error {
        if mr==nil{return fmt.Errorf("runtime missing NodeID %s before commit",id)}
        select{
        case <-mr.done:
            err:=mr.err();if err==nil{err=errors.New("runtime exited before topology commit")}
            return fmt.Errorf("NodeID %s: %w",id,err)
        default:return nil
        }
    }
    for _,id:=range diff.UnchangedNodeIDs{
        if err:=checkRunning(id,c.runtimes[id]);err!=nil{
            c.rollbackStopped(stopped);c.stopMany(staged)
            return clustersync.Snapshot{},false,err
        }
    }
    for _,id:=range added{
        if err:=checkRunning(id,staged[id]);err!=nil{
            c.rollbackStopped(stopped);c.stopMany(staged)
            return clustersync.Snapshot{},false,err
        }
    }

    c.fireTopologyHookForTest(TopologyBeforeCommit,prepared.Snapshot().Generation)
    snap,changed,err:=c.engine.CommitPrepared(prepared)
    if err!=nil{
        c.rollbackStopped(stopped)
        c.stopMany(staged)
        return clustersync.Snapshot{},false,err
    }
    for _,mr:=range stopped{delete(c.runtimes,mr.nodeID);c.lastStopped[mr.nodeID]=mr}
    for id,mr:=range staged{c.runtimes[id]=mr;delete(c.lastStopped,id)}
    c.fireTopologyHookForTest(TopologyAfterCommit,snap.Generation)
    return snap,changed,nil
}

func (c *WorkerController) startManaged(mc clustersync.ManagedNodeConfig) (*managedRuntime,error) {
    runner:=c.newRuntime()
    if runner==nil{return nil,errors.New("runtime factory returned nil")}
    ctx,cancel:=context.WithCancel(c.rootCtx)
    c.nextInstance++
    mr:=&managedRuntime{nodeID:mc.NodeID,managed:mc,runner:runner,cancel:cancel,done:make(chan struct{}),instance:c.nextInstance}
    go func(){
        err:=runner.Run(ctx,mc.Config)
        mr.setErr(err)
        close(mr.done)
        if !mr.planned.Load() && ctx.Err()==nil {
            if err==nil{err=errors.New("runtime stopped unexpectedly")}
            select{case c.failures<-RuntimeFailure{NodeID:mc.NodeID,Instance:mr.instance,Err:err}:default:}
        }
    }()
    timer:=time.NewTimer(runtimeStartupTimeout);defer timer.Stop()
    select{
    case <-runner.DialerReady():
        if err:=runner.DialerReadiness();err!=nil{mr.planned.Store(true);cancel();<-mr.done;return nil,err}
        select{case <-mr.done:
            err:=mr.err();if err==nil{err=errors.New("runtime exited after readiness")}
            return nil,err
        default:}
        return mr,nil
    case <-mr.done:
        err:=mr.err();if err==nil{err=errors.New("runtime exited before readiness")}
        return nil,err
    case <-timer.C:
        mr.planned.Store(true);cancel();<-mr.done
        return nil,errors.New("runtime readiness timed out")
    case <-c.rootCtx.Done():
        mr.planned.Store(true);cancel();<-mr.done
        return nil,c.rootCtx.Err()
    }
}

func (c *WorkerController) stopManaged(mr *managedRuntime) error {
    if mr==nil{return nil}
    mr.planned.Store(true)
    mr.stopOnce.Do(mr.cancel)
    select{
    case <-mr.done:
        return nil
    case <-time.After(runtimeStartupTimeout):
        return fmt.Errorf("runtime %s shutdown timed out",mr.nodeID)
    }
}

func (c *WorkerController) stopMany(ms map[string]*managedRuntime) {
    ids:=make([]string,0,len(ms));for id:=range ms{ids=append(ids,id)};sort.Strings(ids)
    for _,id:=range ids{_ = c.stopManaged(ms[id])}
}

func (c *WorkerController) rollbackStopped(stopped []*managedRuntime) {
    // This path is a defensive commit-failure fallback. Controller serialization
    // makes CommitPrepared failure after reconciliation unreachable in normal use.
    for _,old:=range stopped{
        replacement,err:=c.startManaged(old.managed)
        if err==nil{c.runtimes[old.nodeID]=replacement}
    }
}

func (c *WorkerController) CurrentGeneration() uint64 {
    c.mu.Lock();defer c.mu.Unlock()
    s,ok:=c.engine.Current();if !ok{return 0};return s.Generation
}

func (c *WorkerController) ActiveNodeIDs() []string {
    c.mu.Lock();defer c.mu.Unlock()
    out:=make([]string,0,len(c.runtimes))
    for id,mr:=range c.runtimes{
        select{case <-mr.done:default:out=append(out,id)}
    }
    sort.Strings(out)
    return out
}

func (c *WorkerController) RuntimeFailures() <-chan RuntimeFailure { return c.failures }

func (c *WorkerController) Close() error {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.closed{return nil}
    c.closed=true
    ids:=make([]string,0,len(c.runtimes));for id:=range c.runtimes{ids=append(ids,id)};sort.Strings(ids)
    var first error
    for _,id:=range ids{
        mr:=c.runtimes[id]
        if err:=c.stopManaged(mr);err!=nil&&first==nil{first=err}
        c.lastStopped[id]=mr
        delete(c.runtimes,id)
    }
    c.rootCancel()
    return first
}

func (c *WorkerController) InstanceTokenForTest(nodeID string) (uint64,bool) {
    c.mu.Lock();defer c.mu.Unlock()
    mr,ok:=c.runtimes[nodeID];if !ok{return 0,false};return mr.instance,true
}

func (c *WorkerController) ConfigForTest(nodeID string) (config.Config,bool) {
    c.mu.Lock();defer c.mu.Unlock()
    mr,ok:=c.runtimes[nodeID];if !ok{return config.Config{},false};return mr.managed.Config,true
}

func (c *WorkerController) StoppedDoneForTest(nodeID string) bool {
    c.mu.Lock();defer c.mu.Unlock()
    mr:=c.lastStopped[nodeID];if mr==nil{return false}
    select{case <-mr.done:return true;default:return false}
}

func (c *WorkerController) RecoveryEpochsForTest(nodeID string) []uint64 {
    c.mu.Lock();mr:=c.runtimes[nodeID];c.mu.Unlock()
    if mr==nil{return nil}
    r,ok:=mr.runner.(interface{RecoveryAuthoritiesForTest() []node.RecoveryAuthoritySnapshot});if !ok{return nil}
    states:=r.RecoveryAuthoritiesForTest()
    out:=make([]uint64,0,len(states));for _,s:=range states{out=append(out,s.Epoch)}
    sort.Slice(out,func(i,j int)bool{return out[i]<out[j]})
    return out
}

func (c *WorkerController) SetRuntimeFactoryForTest(fn func() managedRuntimeRunner) {
    c.mu.Lock();defer c.mu.Unlock()
    if fn==nil{c.newRuntime=func() managedRuntimeRunner{return node.NewRuntime()};return}
    c.newRuntime=fn
}


func (c *WorkerController) fireTopologyHookForTest(stage string,generation uint64) {
    if c.topologyHook!=nil { c.topologyHook(stage,generation) }
}

func (c *WorkerController) SetTopologyHookForTest(fn func(string,uint64)) {
    c.mu.Lock();defer c.mu.Unlock()
    c.topologyHook=fn
}

func (c *WorkerController) RuntimeForNodeForTest(nodeID string) (*node.Runtime,bool) {
    c.mu.Lock();defer c.mu.Unlock()
    mr,ok:=c.runtimes[nodeID]
    if !ok{return nil,false}
    r,ok:=mr.runner.(*node.Runtime)
    return r,ok
}

func (c *WorkerController) RecoveryAuthorityForNodeForTest(nodeID string) (node.RecoveryAuthoritySnapshot,bool) {
    c.mu.Lock();mr:=c.runtimes[nodeID];c.mu.Unlock()
    if mr==nil{return node.RecoveryAuthoritySnapshot{},false}
    r,ok:=mr.runner.(interface{RecoveryAuthoritiesForTest() []node.RecoveryAuthoritySnapshot})
    if !ok{return node.RecoveryAuthoritySnapshot{},false}
    states:=r.RecoveryAuthoritiesForTest()
    if len(states)!=1{return node.RecoveryAuthoritySnapshot{},false}
    return states[0],true
}
