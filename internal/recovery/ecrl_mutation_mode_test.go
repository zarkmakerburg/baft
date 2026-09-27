package recovery

import "os"

const mutationEnv = "ECRL_MUTANT"

func mutantActive(name string) bool {
	return os.Getenv(mutationEnv) == name
}

var requiredMutationCatalog = map[string]string{
	"f01_accept_old_epoch":      "F01",
	"f02_accept_both_candidates":"F02",
	"f03_replay_from_k":         "F03",
	"f04_overlap_ring_replay":   "F04",
	"f05_accept_tombstone_open": "F05",
	"f06_lost_fin_never_closes": "F06-LostFIN",
	"f06_apply_fin_twice":       "F06-FIN-idempotence",
	"f07_resume_after_boot_change":"F07",
	"f08_accept_a_gt_s":         "F08",
	"f09_skip_k_le_a":           "F09",
	"f10_replay_from_a_minus_1": "F10",
	"f10_replay_from_a_plus_1":  "F10",
}

func mutantNameForEngine() string {
	return os.Getenv(mutationEnv)
}
