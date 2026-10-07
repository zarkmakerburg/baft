package bcc

import (
	"net/http"
	"sort"
	"time"
)

const (
	PathMatrixPass        = "PASS"
	PathMatrixDegraded    = "DEGRADED"
	PathMatrixFail        = "FAIL"
	PathMatrixStale       = "STALE"
	PathMatrixNotAssessed = "NOT_ASSESSED"
)

type PathMatrixCandidate struct {
	ProbeID       string    `json:"probe_id"`
	Family        string    `json:"family"`
	Port          int       `json:"port"`
	Class         string    `json:"class"`
	State         string    `json:"state"`
	ConnectRTTMS  int64     `json:"connect_rtt_ms,omitempty"`
	ByteCeiling   int64     `json:"byte_ceiling,omitempty"`
	TricklePassed bool      `json:"trickle_passed,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	EvidenceAt    time.Time `json:"evidence_at"`
	AgeSeconds    int64     `json:"age_seconds"`
}

type PathMatrixCell struct {
	SourceNode      string                `json:"source_node"`
	DestinationNode string                `json:"destination_node"`
	State           string                `json:"state"`
	Candidates      []PathMatrixCandidate `json:"candidates,omitempty"`
}

type PathMatrix struct {
	GeneratedAt time.Time        `json:"generated_at"`
	FreshFor    int64            `json:"fresh_for_seconds"`
	Nodes       []string         `json:"nodes"`
	Cells       []PathMatrixCell `json:"cells"`
}

func matrixState(class string) string {
	switch class {
	case PathFullData:
		return PathMatrixPass
	case PathByteCeiling, PathHandshakeOnly, PathConnectOnly:
		return PathMatrixDegraded
	case PathUnreachable, PathLocalConflict:
		return PathMatrixFail
	default:
		return PathMatrixNotAssessed
	}
}

func matrixStateRank(v string) int {
	switch v {
	case PathMatrixPass:
		return 5
	case PathMatrixDegraded:
		return 4
	case PathMatrixFail:
		return 3
	case PathMatrixStale:
		return 2
	default:
		return 1
	}
}

func (s *Store) PathMatrix(now time.Time) PathMatrix {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	active := make(map[string]bool)
	nodes := make([]string, 0, len(s.st.Nodes))
	for id, n := range s.st.Nodes {
		if n.Revoked {
			continue
		}
		active[id] = true
		nodes = append(nodes, id)
	}
	sort.Strings(nodes)

	byPair := make(map[string][]PathMatrixCandidate)
	for _, p := range s.st.PathProbes {
		if p.Phase != PathProbeComplete || !active[p.SourceNode] || !active[p.DestinationNode] || p.SourceNode == p.DestinationNode {
			continue
		}
		for _, c := range p.Candidates {
			if c.Class == "" {
				continue
			}
			state := matrixState(c.Class)
			age := now.Sub(p.UpdatedAt)
			if age < 0 {
				age = 0
			}
			if age > pathDiscoveryFreshFor {
				state = PathMatrixStale
			}
			k := p.SourceNode + "\x00" + p.DestinationNode
			byPair[k] = append(byPair[k], PathMatrixCandidate{
				ProbeID: p.ID, Family: p.Family, Port: c.Port, Class: c.Class, State: state,
				ConnectRTTMS: c.ConnectRTTMS, ByteCeiling: c.ByteCeiling, TricklePassed: c.TricklePassed,
				Detail: c.Detail, EvidenceAt: p.UpdatedAt, AgeSeconds: int64(age / time.Second),
			})
		}
	}

	out := PathMatrix{GeneratedAt: now, FreshFor: int64(pathDiscoveryFreshFor / time.Second), Nodes: nodes}
	for _, src := range nodes {
		for _, dst := range nodes {
			if src == dst {
				continue
			}
			k := src + "\x00" + dst
			cs := byPair[k]
			sort.Slice(cs, func(i, j int) bool {
				if matrixStateRank(cs[i].State) != matrixStateRank(cs[j].State) {
					return matrixStateRank(cs[i].State) > matrixStateRank(cs[j].State)
				}
				if !cs[i].EvidenceAt.Equal(cs[j].EvidenceAt) {
					return cs[i].EvidenceAt.After(cs[j].EvidenceAt)
				}
				if cs[i].Family != cs[j].Family {
					return cs[i].Family < cs[j].Family
				}
				if cs[i].Port != cs[j].Port {
					return cs[i].Port < cs[j].Port
				}
				return cs[i].ProbeID < cs[j].ProbeID
			})
			state := PathMatrixNotAssessed
			if len(cs) > 0 {
				state = cs[0].State
			}
			out.Cells = append(out.Cells, PathMatrixCell{SourceNode: src, DestinationNode: dst, State: state, Candidates: cs})
		}
	}
	return out
}

func (s *Server) pathMatrixAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.PathMatrix(s.now()))
}
