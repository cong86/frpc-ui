package observe

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestRuntimeDiscoveryUsesExactNamesAndHandlesAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		docker, service string
		dockerErr       error
		count, issues   int
	}{
		{"nginx\n", "not-found\n", nil, 1, 0},
		{"", "loaded\n", nil, 1, 0},
		{"nginx\n", "loaded\n", nil, 2, 0},
		{"", "not-found\n", nil, 0, 0},
		{"", "loaded\n", errors.New("private-command-error"), 1, 1},
		{"", "loaded\n", os.ErrNotExist, 1, 0},
	} {
		run := func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "docker":
				if !reflect.DeepEqual(args, []string{"container", "ls", "-a", "--filter", "name=^/nginx$", "--format", "{{.Names}}"}) {
					t.Fatal("unbounded container enumeration", args)
				}
				return []byte(tc.docker), tc.dockerErr
			case "systemctl":
				if !reflect.DeepEqual(args, []string{"show", "nginx.service", "--property=LoadState", "--value"}) {
					t.Fatal("unexpected service probe", args)
				}
				return []byte(tc.service), nil
			default:
				t.Fatal("unexpected executable")
			}
			return nil, nil
		}
		got := detectExistingRuntime(context.Background(), "nginx", run)
		if len(got.Candidates) != tc.count || len(got.Issues) != tc.issues {
			t.Fatal("wrong runtime evidence", got)
		}
	}
	detectExistingRuntime(context.Background(), "nginx; reboot", func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("invalid target reached executable")
		return nil, nil
	})
}
