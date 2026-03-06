package rclonestorage

import (
	"strings"
	"sync"

	"github.com/unknwon/goconfig"
)

var saveConfigData = goconfig.SaveConfigData

// memoryStorage is an in-memory implementation of rclone's config.Storage.
type memoryStorage struct {
	mu   sync.RWMutex
	conf *goconfig.ConfigFile
}

func newMemoryStorage(data string) (*memoryStorage, error) {
	conf, err := goconfig.LoadFromReader(strings.NewReader(data))
	if err != nil {
		return nil, err
	}
	return &memoryStorage{conf: conf}, nil
}

// GetSectionList returns a slice of strings with names for all the sections.
func (m *memoryStorage) GetSectionList() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conf.GetSectionList()
}

// HasSection returns true if section exists in the config file.
func (m *memoryStorage) HasSection(section string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.conf.GetSectionList() {
		if s == section {
			return true
		}
	}
	return false
}

// DeleteSection removes the named section and all config from the config file.
func (m *memoryStorage) DeleteSection(section string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conf.DeleteSection(section)
}

// GetKeyList returns the keys in this section.
func (m *memoryStorage) GetKeyList(section string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conf.GetKeyList(section)
}

// GetValue returns the key in section with a found flag.
func (m *memoryStorage) GetValue(section string, key string) (value string, found bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, err := m.conf.GetValue(section, key)
	if err != nil {
		return "", false
	}
	return val, true
}

// SetValue sets the value under key in section.
func (m *memoryStorage) SetValue(section string, key string, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.conf.SetValue(section, key, value)
}

// DeleteKey removes the key under section.
func (m *memoryStorage) DeleteKey(section string, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conf.DeleteKey(section, key)
}

// Load the config from permanent storage (noop for memory).
func (m *memoryStorage) Load() error {
	return nil
}

// Save the config to permanent storage (noop for memory).
func (m *memoryStorage) Save() error {
	return nil
}

// Serialize the config into a string.
func (m *memoryStorage) Serialize() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var b strings.Builder
	if err := saveConfigData(m.conf, &b); err != nil {
		return "", err
	}
	return b.String(), nil
}
