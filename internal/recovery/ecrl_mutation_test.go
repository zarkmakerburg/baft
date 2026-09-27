package recovery

import "testing"

func TestECRLMutationCatalogCoversEveryF(t *testing.T){
	seen:=map[string]bool{}
	for _,f:=range requiredMutationCatalog{seen[f]=true}
	for _,want:=range []string{"F01","F02","F03","F04","F05","F06-LostFIN","F07","F08","F09","F10"}{
		if !seen[want]{t.Fatalf("mutation catalog has no mutant for %s",want)}
	}
}
