package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAllowsMissingAndEmptyConfigDirectory(t *testing.T) {
	for _, name := range []string{"missing", "empty"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			global := filepath.Join(root, "hookfly.yaml")
			writeDiscoveryFile(t, global, []byte(globalYAML("conf.d")))
			if name == "empty" {
				if err := os.Mkdir(filepath.Join(root, "conf.d"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			candidate, err := Discover(global)
			if err != nil || len(candidate.Resources) != 0 {
				t.Fatalf("Discover() resources/error = %d/%v", len(candidate.Resources), err)
			}
		})
	}
}

func TestDiscoverUsesDefaultConfigDirectory(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "hookfly.yaml")
	resource := filepath.Join(root, "conf.d", "routes.yaml")
	writeDiscoveryFile(t, global, []byte("kind: Hookfly\nhistory: {}\npolling: {}\n"))
	writeDiscoveryFile(t, resource, []byte("kind: Routes\n"))

	candidate, err := Discover(global)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resourcePaths(candidate.Resources), []string{resource}; !sameStrings(got, want) {
		t.Fatalf("Discover() resource paths = %q, want %q", got, want)
	}
}

func TestDiscoverSelectsDirectRegularYAMLFilesInPathOrder(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "hookfly.yaml")
	resources := filepath.Join(root, "conf.d")
	writeDiscoveryFile(t, global, []byte(globalYAML("conf.d")))
	if err := os.Mkdir(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFile(t, filepath.Join(resources, "z.yaml"), []byte("z"))
	writeDiscoveryFile(t, filepath.Join(resources, "a.yaml"), []byte("a"))
	writeDiscoveryFile(t, filepath.Join(resources, "README.txt"), []byte("ignored"))
	if err := os.Mkdir(filepath.Join(resources, "nested.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFile(t, filepath.Join(resources, "nested.yaml", "inside.yaml"), []byte("ignored"))
	if err := os.Symlink(filepath.Join(resources, "a.yaml"), filepath.Join(resources, "link.yaml")); err != nil {
		t.Fatal(err)
	}

	candidate, err := Discover(global)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resourcePaths(candidate.Resources), []string{
		filepath.Join(resources, "a.yaml"),
		filepath.Join(resources, "z.yaml"),
	}; !sameStrings(got, want) {
		t.Fatalf("Discover() resource paths = %q, want %q", got, want)
	}
}

func TestDiscoverRejectsChangedConfigurationDirectory(t *testing.T) {
	filesystem := changingDiscoveryFilesystem{
		files: map[string][]byte{
			"/cfg/hookfly.yaml":  []byte(globalYAML("conf.d")),
			"/cfg/conf.d/a.yaml": []byte("a"),
		},
		reads: [][]fs.DirEntry{
			{discoveryEntry{name: "a.yaml"}},
			{discoveryEntry{name: "a.yaml"}, discoveryEntry{name: "b.yaml"}},
		},
	}

	_, err := discoverWithFilesystem("/cfg/hookfly.yaml", &filesystem)
	if err == nil || !strings.Contains(err.Error(), "configuration directory changed during discovery") {
		t.Fatalf("discoverWithFilesystem() error = %v, want directory mutation error", err)
	}
}

func writeDiscoveryFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func resourcePaths(resources []SourceFile) []string {
	paths := make([]string, len(resources))
	for index := range resources {
		paths[index] = resources[index].Path
	}
	return paths
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type changingDiscoveryFilesystem struct {
	files map[string][]byte
	reads [][]fs.DirEntry
	read  int
}

func (filesystem *changingDiscoveryFilesystem) ReadFile(path string) ([]byte, error) {
	content, ok := filesystem.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return content, nil
}

func (filesystem *changingDiscoveryFilesystem) OpenDirectory(path string) (discoveryDirectory, error) {
	return &changingDiscoveryDirectory{filesystem: filesystem, path: path}, nil
}

func (filesystem *changingDiscoveryFilesystem) ReadDir(string) ([]fs.DirEntry, error) {
	if filesystem.read >= len(filesystem.reads) {
		return nil, errors.New("unexpected directory read")
	}
	entries := filesystem.reads[filesystem.read]
	filesystem.read++
	return entries, nil
}

type changingDiscoveryDirectory struct {
	filesystem *changingDiscoveryFilesystem
	path       string
}

func (directory *changingDiscoveryDirectory) ReadDir() ([]fs.DirEntry, error) {
	return directory.filesystem.ReadDir(directory.path)
}

func (directory *changingDiscoveryDirectory) ReadFile(name string) ([]byte, error) {
	return directory.filesystem.ReadFile(filepath.Join(directory.path, name))
}

func (*changingDiscoveryDirectory) Close() error { return nil }

type discoveryEntry struct{ name string }

func (entry discoveryEntry) Name() string               { return entry.name }
func (discoveryEntry) IsDir() bool                      { return false }
func (discoveryEntry) Type() fs.FileMode                { return 0 }
func (entry discoveryEntry) Info() (fs.FileInfo, error) { return nil, errors.New("not needed") }
