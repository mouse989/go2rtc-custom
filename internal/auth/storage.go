package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

type userStore struct {
	mu    sync.RWMutex
	path  string
	users map[string]*User
}

var store *userStore

// jwtSecretRef is set by initSecret so we can use it for stream ID hashing
var jwtSecretRef []byte

func initStore(path string) error {
	store = &userStore{
		path:  path,
		users: make(map[string]*User),
	}
	return store.load()
}

func (s *userStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s.seedAdmin()
	}
	if err != nil {
		return err
	}

	var users []*User
	if err = json.Unmarshal(data, &users); err != nil {
		return err
	}

	s.users = make(map[string]*User, len(users))
	for _, u := range users {
		s.users[u.Username] = u
	}
	return nil
}

func (s *userStore) seedAdmin() error {
	hash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	admin := &User{
		Username: "admin",
		Password: string(hash),
		Role:     RoleAdmin,
		Enabled:  true,
	}
	s.users["admin"] = admin
	return s.saveUnlocked()
}

func (s *userStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveUnlocked()
}

func (s *userStore) saveUnlocked() error {
	list := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// GetUser returns a copy of the user — caller may modify it freely.
func GetUser(username string) (*User, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	u, ok := store.users[username]
	if !ok {
		return nil, false
	}
	cp := *u
	return &cp, true
}

// ListUsers returns all users with passwords redacted.
func ListUsers() []*User {
	store.mu.RLock()
	defer store.mu.RUnlock()
	list := make([]*User, 0, len(store.users))
	for _, u := range store.users {
		cp := *u
		cp.Password = ""
		list = append(list, &cp)
	}
	return list
}

// CreateUser hashes plainPassword and stores a NEW copy — caller's pointer is safe to modify.
func CreateUser(u *User, plainPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	// Store an independent copy so the caller can do whatever with u after this.
	stored := *u
	stored.Password = string(hash)

	store.mu.Lock()
	store.users[stored.Username] = &stored
	store.mu.Unlock()

	return store.save()
}

// UpdateUser updates fields from u and optionally changes the password.
// Stores an independent copy — caller's pointer is safe to modify afterwards.
func UpdateUser(u *User, plainPassword string) error {
	store.mu.Lock()
	existing, ok := store.users[u.Username]
	if !ok {
		store.mu.Unlock()
		return errNotFound
	}

	// Build an independent stored copy
	stored := *u
	if plainPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.DefaultCost)
		if err != nil {
			store.mu.Unlock()
			return err
		}
		stored.Password = string(hash)
	} else {
		stored.Password = existing.Password // keep current hash
	}
	store.users[stored.Username] = &stored
	store.mu.Unlock()

	return store.save()
}

// DeleteUser removes a user by username.
func DeleteUser(username string) error {
	store.mu.Lock()
	if _, ok := store.users[username]; !ok {
		store.mu.Unlock()
		return errNotFound
	}
	delete(store.users, username)
	store.mu.Unlock()
	return store.save()
}

// Authenticate checks credentials and returns a copy of the user on success.
func Authenticate(username, password string) (*User, bool) {
	store.mu.RLock()
	u, ok := store.users[username]
	store.mu.RUnlock()

	if !ok || !u.Enabled {
		return nil, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password)) != nil {
		return nil, false
	}
	cp := *u
	return &cp, true
}

// StreamID returns a stable 12-char hex ID for a stream name.
// Uses HMAC-SHA256 of the JWT secret so IDs are server-specific and not guessable.
func StreamID(streamName string) string {
	h := sha256.New()
	h.Write(jwtSecretRef)
	h.Write([]byte(streamName))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// streamIDToName is the reverse lookup built on startup / stream change.
// key = 12-char hex ID, value = real stream name.
// Populated externally by the streams package after init.
var streamIDMap = map[string]string{}
var streamIDMu sync.RWMutex

// RegisterStreamID adds/updates a stream→ID mapping.
func RegisterStreamID(name string) string {
	id := StreamID(name)
	streamIDMu.Lock()
	streamIDMap[id] = name
	streamIDMu.Unlock()
	return id
}

// StreamNameByID resolves a masked ID back to the real stream name.
func StreamNameByID(id string) (string, bool) {
	streamIDMu.RLock()
	name, ok := streamIDMap[id]
	streamIDMu.RUnlock()
	return name, ok
}
