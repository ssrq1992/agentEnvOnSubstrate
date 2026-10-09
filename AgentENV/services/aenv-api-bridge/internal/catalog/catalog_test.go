package catalog

import (
	"math"
	"strings"
	"testing"
)

func TestCanonicalReservationDigest(t *testing.T) {
	a := Layer{Digest: strings.Repeat("a", 64), Size: 10}
	b := Layer{Digest: strings.Repeat("b", 64), Size: 20}
	_, first, err := canonicalLayers([]Layer{a, b})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := canonicalLayers([]Layer{b, a})
	if err != nil || first != second {
		t.Fatal("layer order changed operation identity")
	}
	b.Size++
	_, changed, err := canonicalLayers([]Layer{a, b})
	if err != nil || changed == first {
		t.Fatal("changed payload retained digest")
	}
	if _, _, err := canonicalLayers([]Layer{a, a}); err == nil {
		t.Fatal("duplicate layer accepted")
	}
}
func TestObjectKeysCannotEscapeCatalogPrefix(t *testing.T) {
	for _, digest := range []string{"../delete", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		if _, err := ObjectKey(digest); err == nil {
			t.Fatal("invalid object digest accepted")
		}
	}
	key, err := ObjectKey(strings.Repeat("a", 64))
	if err != nil || key != "sha256/aa/"+strings.Repeat("a", 64) {
		t.Fatal("non-canonical object key")
	}
}
func TestRuntimePinRequiresFullBoundedFence(t *testing.T) {
	pin := Pin{Tenant: "tenant", ActorUID: "actor", Generation: 1, WorkerEpoch: 1, WorkerPodUID: "pod", ExecutorInstanceID: "executor"}
	if err := validatePin(pin); err != nil {
		t.Fatal(err)
	}
	pin.Generation = uint64(math.MaxInt64) + 1
	if err := validatePin(pin); err == nil {
		t.Fatal("generation exceeds PostgreSQL bigint")
	}
	pin.Generation = 0
	if err := validatePin(pin); err == nil {
		t.Fatal("zero generation accepted")
	}
	pin.Generation = 1
	pin.ExecutorInstanceID = ""
	if err := validatePin(pin); err == nil {
		t.Fatal("missing process identity accepted")
	}
}
