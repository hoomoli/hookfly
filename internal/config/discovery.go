package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

type discoveryFilesystem interface {
	ReadFile(string) ([]byte, error)
	OpenDirectory(string) (discoveryDirectory, error)
}

type discoveryDirectory interface {
	ReadDir() ([]fs.DirEntry, error)
	ReadFile(string) ([]byte, error)
	Close() error
}

type osDiscoveryFilesystem struct{}

func (osDiscoveryFilesystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osDiscoveryFilesystem) OpenDirectory(path string) (discoveryDirectory, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &osDiscoveryDirectory{root: root}, nil
}

type osDiscoveryDirectory struct {
	root *os.Root
}

func (directory *osDiscoveryDirectory) ReadDir() ([]fs.DirEntry, error) {
	root, err := directory.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadDir(-1)
}

func (directory *osDiscoveryDirectory) ReadFile(name string) ([]byte, error) {
	return directory.root.ReadFile(name)
}

func (directory *osDiscoveryDirectory) Close() error {
	return directory.root.Close()
}

// Discover reads the required global configuration and snapshots direct resource files.
func Discover(globalPath string) (Candidate, error) {
	return discoverWithFilesystem(globalPath, osDiscoveryFilesystem{})
}

func discoverWithFilesystem(globalPath string, filesystem discoveryFilesystem) (Candidate, error) {
	globalPath = filepath.Clean(globalPath)
	globalContent, err := filesystem.ReadFile(globalPath)
	if err != nil {
		return Candidate{}, fmt.Errorf("read global configuration %q: %w", globalPath, err)
	}
	global := SourceFile{Path: globalPath, Content: globalContent}
	header, err := decodeGlobalHeader(global)
	if err != nil {
		return Candidate{}, err
	}

	resourceDirectory := header.ConfigDir
	if resourceDirectory == "" {
		resourceDirectory = "conf.d"
	}
	if !filepath.IsAbs(resourceDirectory) {
		resourceDirectory = filepath.Join(filepath.Dir(globalPath), resourceDirectory)
	}
	resourceDirectory = filepath.Clean(resourceDirectory)
	directory, err := filesystem.OpenDirectory(resourceDirectory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Candidate{Global: global}, nil
		}
		return Candidate{}, fmt.Errorf("read configuration directory %q: %w", resourceDirectory, err)
	}
	defer directory.Close()
	first, err := resourceNames(directory)
	if err != nil {
		return Candidate{}, fmt.Errorf("read configuration directory %q: %w", resourceDirectory, err)
	}

	resources := make([]SourceFile, 0, len(first))
	for _, name := range first {
		path := filepath.Join(resourceDirectory, name)
		content, err := directory.ReadFile(name)
		if err != nil {
			return Candidate{}, fmt.Errorf("read configuration resource %q: %w", path, err)
		}
		resources = append(resources, SourceFile{Path: path, Content: content})
	}
	second, err := resourceNames(directory)
	if err != nil || !sameResourceNames(first, second) {
		return Candidate{}, fmt.Errorf("configuration directory changed during discovery: %q", resourceDirectory)
	}
	return Candidate{Global: global, Resources: resources}, nil
}

func decodeGlobalHeader(source SourceFile) (globalDiscoveryHeader, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(source.Content))
	decoder.KnownFields(true)
	var header globalDiscoveryHeader
	if err := decoder.Decode(&header); err != nil {
		return globalDiscoveryHeader{}, fmt.Errorf("decode global configuration %q: %w", source.Path, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return globalDiscoveryHeader{}, fmt.Errorf("decode global configuration %q: multiple YAML documents are not supported", source.Path)
	} else if !errors.Is(err, io.EOF) {
		return globalDiscoveryHeader{}, fmt.Errorf("decode global configuration %q: %w", source.Path, err)
	}
	return header, nil
}

type globalDiscoveryHeader struct {
	Kind      string    `yaml:"kind"`
	ConfigDir string    `yaml:"config_dir"`
	History   yaml.Node `yaml:"history"`
	Polling   yaml.Node `yaml:"polling"`
}

func resourceNames(directory discoveryDirectory) ([]string, error) {
	entries, err := directory.ReadDir()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && filepath.Ext(entry.Name()) == ".yaml" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func sameResourceNames(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
