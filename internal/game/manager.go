package game

import (
	"sync"

	"github.com/zzqDeco/teamie-werewolf/internal/auth"
)

// RoomKey uniquely identifies a Teamie group conversation room.
type RoomKey struct {
	WorkspaceID    string
	ConversationID string
}

// Manager lazily manages Werewolf rooms keyed by (workspaceId, conversationId).
// Read-only inspections on non-existent rooms NEVER allocate state, guaranteeing
// zero dirty room side effects during 4-stage live protocol probes.
type Manager struct {
	mu              sync.RWMutex
	rooms           map[RoomKey]*Room
	timings         Timings
	shuffleFn       func([]Role)
	onNotifications func([]TargetedNotification)
	onRoomUpdated   func(workspaceID, conversationID string)
}

// NewManager creates a room manager with the given default timings.
func NewManager(timings Timings) *Manager {
	if timings.NightTimeout <= 0 {
		timings = DefaultTimings()
	}
	return &Manager{
		rooms:   make(map[RoomKey]*Room),
		timings: timings,
	}
}

// SetCallbacks wires MCP targeted notification and Web UI stream update listeners.
func (m *Manager) SetCallbacks(
	onNotifications func([]TargetedNotification),
	onRoomUpdated func(workspaceID, conversationID string),
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onNotifications = onNotifications
	m.onRoomUpdated = onRoomUpdated
}

// SetShuffleFunc sets a deterministic role shuffle function for newly created rooms (testing only).
func (m *Manager) SetShuffleFunc(fn func([]Role)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shuffleFn = fn
}

// RoomCount returns the number of allocated rooms (used in tests to verify zero probe side effects).
func (m *Manager) RoomCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

// GetView returns the caller's Fog-of-War view without creating a Room if none exists.
func (m *Manager) GetView(claims auth.Claims) PlayerView {
	key := RoomKey{WorkspaceID: claims.WorkspaceID, ConversationID: claims.ConversationID}
	m.mu.RLock()
	room := m.rooms[key]
	timings := m.timings
	m.mu.RUnlock()
	if room == nil {
		return EmptyLobbyView(claims, timings)
	}
	return room.GetView(claims)
}

// GetOrCreateRoom returns an existing room or allocates one for write operations.
func (m *Manager) GetOrCreateRoom(workspaceID, conversationID string) *Room {
	key := RoomKey{WorkspaceID: workspaceID, ConversationID: conversationID}
	m.mu.RLock()
	if room, ok := m.rooms[key]; ok {
		m.mu.RUnlock()
		return room
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if room, ok := m.rooms[key]; ok {
		return room
	}
	room := NewRoom(workspaceID, conversationID, m.timings, m.onNotifications, m.onRoomUpdated)
	if m.shuffleFn != nil {
		room.SetShuffleFunc(m.shuffleFn)
	}
	m.rooms[key] = room
	return room
}

// CloseAll stops timers across all active rooms on server shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.Unlock()
	for _, r := range rooms {
		r.Close()
	}
}
