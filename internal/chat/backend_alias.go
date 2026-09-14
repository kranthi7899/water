package chat

import "water/internal/backend"

// backendIface keeps the RoleBackends map type readable in commands.go.
type backendIface = backend.Backend
