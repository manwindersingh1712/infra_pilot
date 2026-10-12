# Database Connection Details Panel

**Date:** 2026-04-13
**Status:** Approved for implementation

## Overview

Add a "Connection" tab to the Service Details Panel that displays connection information for database services (Redis, MongoDB). This allows users to connect to their deployed databases using their preferred client tools.

## Goals

- Display connection URL, host, port, and password for database services
- Provide copy-to-clipboard functionality for all connection fields
- Show CLI connection commands for quick terminal access
- Hide sensitive credentials (passwords) by default with reveal toggle

## Non-Goals

- Web-based database browser/query interface (out of scope for minimal control-plane)
- Built-in terminal/shell access (can be added later if requested)
- Connection testing functionality

## Architecture

### Data Flow

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  Deploy     │────▶│  Store      │────▶│  API        │
│  Worker     │     │  Runtime    │     │  Endpoint   │
└─────────────┘     │  Config     │     └──────┬──────┘
                    └─────────────┘            │
                                               │
                    ┌─────────────┐            │
                    │  UI Panel   │◄───────────┘
                    │  (Display)  │
                    └─────────────┘
```

### Backend Changes

**1. Extend Deployment Model**
Store connection details when database deployment completes:
- Add `connectionInfo` JSON field to `Deployment` model (optional)
- Or derive from runtime config + service metadata

**2. New API Endpoint**
`GET /services/:id/connection`
```typescript
interface ConnectionInfo {
  url: string;           // Full connection URL
  host: string;
  port: number;
  password: string;
  database?: string;     // For MongoDB
  username?: string;
}

// Response
{
  serviceType: "redis" | "mongodb",
  connection: ConnectionInfo,
  cliCommand: string,    // e.g., "redis-cli -h host -p port"
}
```

### Frontend Changes

**1. ServiceDetailsPanel.tsx**
- Add "Connection" tab alongside Deployments/Logs/Env Vars
- Only show tab for database service types (redis, mongodb)

**2. DatabaseConnectionPanel.tsx (new)**
- Display connection fields with copy buttons
- Password reveal toggle (hidden by default)
- CLI command box with copy button
- Styled cards for different connection methods

### UI Design

```
┌─ Service Details: my-redis ─────────┐
│ [Deployments] [Logs] [Env] [Connection]│
│                                      │
│  🔗 Connection Details               │
│                                      │
│  ┌─ Connection URL ─────────────────┐│
│  │ redis://localhost:6379            ││
│  │                          [Copy]   ││
│  └──────────────────────────────────┘│
│                                      │
│  Host:     localhost                 │
│  Port:     6379                      │
│  Password: ••••••••         [Show]   │
│                                      │
│  ┌─ Connect via CLI ────────────────┐│
│  │ $ redis-cli -h localhost -p 6379 ││
│  │                          [Copy]   ││
│  └──────────────────────────────────┘│
│                                      │
│  Need a GUI client?                  │
│  → TablePlus, MongoDB Compass, etc   │
└──────────────────────────────────────┘
```

## Implementation Plan

### Phase 1: Backend
1. Add `connectionInfo` to Deployment model in Prisma schema
2. Update deploy worker to store connection details for databases
3. Create `GET /services/:id/connection` endpoint
4. Return connection info from runtime config

### Phase 2: Frontend
1. Create `DatabaseConnectionPanel.tsx` component
2. Add "Connection" tab to `ServiceDetailsPanel`
3. Implement copy-to-clipboard functionality
4. Add password reveal toggle

### Phase 3: Testing
1. Deploy Redis service, verify connection info appears
2. Deploy MongoDB service, verify connection info appears
3. Test copy buttons work
4. Test password reveal/hide

## Files to Modify

| File | Change |
|------|--------|
| `control-plane/prisma/schema.prisma` | Add `connectionInfo` JSON to Deployment |
| `control-plane/apps/api/src/routes/services.ts` | Add `GET /services/:id/connection` endpoint |
| `control-plane/apps/worker/src/handlers/deploy.ts` | Store connection info after db deployment |
| `cp-ui/src/components/ServiceDetailsPanel.tsx` | Add Connection tab |
| `cp-ui/src/components/DatabaseConnectionPanel.tsx` | New component (create) |

## Success Criteria

- [ ] Redis services show connection URL, host, port, password
- [ ] MongoDB services show connection URL, host, port, password, database
- [ ] All fields have working copy-to-clipboard buttons
- [ ] Passwords are hidden by default, can be revealed
- [ ] CLI connection command is displayed and copyable
- [ ] Connection tab only appears for database service types
- [ ] Non-database services don't show the Connection tab
