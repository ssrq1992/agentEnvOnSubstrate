package layers

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"agentenv/services/aenv-api-bridge/internal/catalog"
)

type dependencies struct {
	events []string
	fail   string
}

func (d *dependencies) event(name string) error {
	d.events = append(d.events, name)
	if d.fail == name {
		return errors.New("injected failure")
	}
	return nil
}
func (d *dependencies) Reserve(context.Context, string, string, []catalog.Layer) error {
	return d.event("reserve")
}
func (d *dependencies) ConfirmUploaded(context.Context, string, string, catalog.Layer) error {
	return d.event("confirm")
}
func (d *dependencies) CommitOwner(context.Context, string, catalog.Owner) error {
	return d.event("commit")
}
func (d *dependencies) Upload(context.Context, catalog.Layer, string) error { return d.event("upload") }
func TestPublicationRetainsUnknownEffects(t *testing.T) {
	for _, tc := range []struct {
		fail string
		want []string
	}{{"reserve", []string{"reserve"}}, {"upload", []string{"reserve", "upload"}}, {"confirm", []string{"reserve", "upload", "confirm"}}, {"", []string{"reserve", "upload", "confirm"}}} {
		t.Run(tc.fail, func(t *testing.T) {
			d := &dependencies{fail: tc.fail}
			p := &Publisher{Catalog: d, Objects: d}
			err := p.Stage(t.Context(), "tenant", "operation", []File{{Layer: catalog.Layer{Digest: strings.Repeat("a", 64), Size: 1}, Path: "/private/snapshot/layer"}})
			if (err == nil) != (tc.fail == "") || !reflect.DeepEqual(d.events, tc.want) {
				t.Fatalf("ordering %v, error %v", d.events, err)
			}
			if tc.fail == "" {
				if err := p.CommitConfirmed(t.Context(), "operation", catalog.Owner{Tenant: "tenant", Kind: "snapshot", UID: "snapshot"}); err != nil {
					t.Fatal(err)
				}
				if d.events[len(d.events)-1] != "commit" {
					t.Fatal("owner not committed")
				}
			}
		})
	}
}
func TestInvalidFilesHaveNoEffects(t *testing.T) {
	d := &dependencies{}
	p := &Publisher{Catalog: d, Objects: d}
	file := File{Layer: catalog.Layer{Digest: strings.Repeat("a", 64), Size: 1}, Path: "/private/layer"}
	if err := p.Stage(t.Context(), "tenant", "operation", []File{file, file}); err == nil || len(d.events) != 0 {
		t.Fatal("duplicate upload had effects")
	}
}
