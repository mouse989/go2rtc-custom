package auth

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
)

// Group represents a logical camera group used for display and bulk permissions.
type Group struct {
	Name    string   `json:"name"`
	Cameras []string `json:"cameras"`
}

type groupStore struct {
	mu     sync.RWMutex
	path   string
	groups map[string]*Group
}

var gstore *groupStore

func initGroups(path string) error {
	gstore = &groupStore{
		path:   path,
		groups: make(map[string]*Group),
	}
	return gstore.load()
}

func (s *groupStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil // empty groups on first run
	}
	if err != nil {
		return err
	}
	var groups []*Group
	if err = json.Unmarshal(data, &groups); err != nil {
		return err
	}
	s.groups = make(map[string]*Group, len(groups))
	for _, g := range groups {
		s.groups[g.Name] = g
	}
	return nil
}

func (s *groupStore) save() error {
	list := make([]*Group, 0, len(s.groups))
	for _, g := range s.groups {
		list = append(list, g)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// ListGroups returns all groups sorted by name. Each is a defensive copy.
func ListGroups() []*Group {
	gstore.mu.RLock()
	defer gstore.mu.RUnlock()
	list := make([]*Group, 0, len(gstore.groups))
	for _, g := range gstore.groups {
		cp := *g
		cams := make([]string, len(g.Cameras))
		copy(cams, g.Cameras)
		cp.Cameras = cams
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// GetGroup returns a copy of the named group.
func GetGroup(name string) (*Group, bool) {
	gstore.mu.RLock()
	defer gstore.mu.RUnlock()
	g, ok := gstore.groups[name]
	if !ok {
		return nil, false
	}
	cp := *g
	cams := make([]string, len(g.Cameras))
	copy(cams, g.Cameras)
	cp.Cameras = cams
	return &cp, true
}

// CreateGroup adds a new group.
func CreateGroup(g *Group) error {
	stored := *g
	gstore.mu.Lock()
	gstore.groups[stored.Name] = &stored
	gstore.mu.Unlock()
	return gstore.save()
}

// UpdateGroup replaces an existing group (creates if not found).
func UpdateGroup(g *Group) error {
	stored := *g
	gstore.mu.Lock()
	gstore.groups[stored.Name] = &stored
	gstore.mu.Unlock()
	return gstore.save()
}

// RenameGroup renames a group (atomic delete+create).
func RenameGroup(oldName, newName string, cameras []string) error {
	gstore.mu.Lock()
	delete(gstore.groups, oldName)
	gstore.groups[newName] = &Group{Name: newName, Cameras: cameras}
	gstore.mu.Unlock()
	return gstore.save()
}

// DeleteGroup removes a group by name (no error if not found).
func DeleteGroup(name string) error {
	gstore.mu.Lock()
	delete(gstore.groups, name)
	gstore.mu.Unlock()
	return gstore.save()
}
