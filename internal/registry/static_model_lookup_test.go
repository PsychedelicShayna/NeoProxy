package registry

import (
	"reflect"
	"testing"
)

// Static lookups of Meta ids resolve to NeoPi's bundled Muse Code and Model API rows.
func TestLookupStaticModelInfoUsesNeoPiMetaCatalog(t *testing.T) {
	models := GetMetaModels()
	if len(models) == 0 {
		t.Fatal("no Meta models")
	}
	for _, model := range models {
		if got := LookupStaticModelInfo(model.ID); !reflect.DeepEqual(got, model) {
			t.Errorf("lookup metadata differs from the NeoPi Meta catalog for %q", model.ID)
		}
	}
	if LookupStaticModelInfo("muse-spark-1.3") == nil || LookupStaticModelInfo("muse-wonder-full-1.0") == nil {
		t.Fatal("NeoPi's Muse Spark / Muse Code rows are missing from the static lookup")
	}
}
