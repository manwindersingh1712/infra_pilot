# Infra Pilot - Node.js Deployment Guide

This document explains how Infra Pilot handles Node.js application deployments, including the build process, Dockerfile generation, environment variables, and troubleshooting.

## Overview

Infra Pilot can deploy Node.js applications from any Git repository without requiring a Dockerfile. It auto-detects the application configuration from `package.json` and generates a production-ready Dockerfile.

## Service Types

### 1. Node.js Service (`serviceType: "nodejs"`)
- **Git Repository**: Required (must contain `package.json`)
- **Dockerfile**: Auto-generated if not present
- **Build Process**: Clones repo → Generates Dockerfile → Builds image → Pushes to registry

### 2. MongoDB Service (`serviceType: "mongodb"`)
- **Git Repository**: Not required
- **Image**: Uses official `mongo:7` image
- **Persistence**: Data stored in volume at `/tmp/cp-volumes/{serviceId}`
- **Default Port**: 27017

### 3. Redis Service (`serviceType: "redis"`)
- **Git Repository**: Not required
- **Image**: Uses official `redis:7-alpine` image
- **Persistence**: Data stored in volume at `/tmp/cp-volumes/{serviceId}`
- **Default Port**: 6379

### 4. Docker Service (`serviceType: "docker"`)
- **Git Repository**: Required (must contain `Dockerfile`)
- **Dockerfile**: Must exist in repository

## Build Pipeline

### Step 1: Clone Repository
```bash
git clone --depth 1 --branch {branch} {repoUrl} /tmp/cp-builds/{deploymentId}/repo
```

### Step 2: Generate Dockerfile (Node.js only)

If `Dockerfile` doesn't exist, Infra Pilot generates one based on `package.json`:

```dockerfile
FROM node:20-alpine

WORKDIR /app

# Install dependencies first (better caching)
COPY package*.json ./
RUN npm ci --only=production

# Copy source code
COPY . .

# Build application (if build script exists)
RUN npm run build

# Expose port
EXPOSE 3000

# Start application
CMD ["sh", "-c", "node app.js"]
```

**Special Handling:**
- If `scripts.start` contains `nodemon`, it's replaced with `node` (nodemon is a devDependency)
- Detects `engines.node` for Node.js version (defaults to 20-alpine)
- Runs `npm run build` if `scripts.build` exists

### Step 3: Build Docker Image
```bash
docker build -t {registry}/{serviceId}:{commitSha} .
```

### Step 4: Push to Registry
```bash
docker push {registry}/{serviceId}:{commitSha}
```

### Step 5: Deploy Container
```bash
docker run -d \
  --name cp-{deploymentId} \
  --network cp_net \
  -p {hostPort}:{containerPort} \
  -e KEY1=value1 \
  -e KEY2=value2 \
  {registry}/{serviceId}:{commitSha}
```

## Environment Variables

### How They Work
- **Storage**: Stored in database (`EnvVar` table)
- **Injection**: Passed via `-e` flags to `docker run`
- **No Rebuild Required**: Changing env vars only restarts the container

### Setting Environment Variables

1. Select your service in the UI
2. Go to "Environment Variables" section
3. Add Key-Value pairs
4. Click "Trigger Deploy" to apply changes

### Example: MongoDB Connection

For a Node.js app connecting to MongoDB:

| Key | Value |
|-----|-------|
| `DATABASE_URL` | `mongodb://{mongo-service-id}:27017/mydb` |
| `PORT` | `3000` |
| `NODE_ENV` | `production` |

**Note**: The `{mongo-service-id}` is the internal hostname of your MongoDB service within the Docker network.

## Common Issues and Solutions

### 1. Container Exits Immediately (Crash Loop)

**Symptom**: Container status shows `Exited` in `docker ps -a`

**Cause**: Application crashes on startup (usually can't connect to database)

**Fix**:
```bash
# Check logs
docker logs cp-{deploymentId}

# Common fix: Set correct DATABASE_URL
# In UI, add env var:
# DATABASE_URL=mongodb://{your-mongo-service-id}:27017/mydb
```

### 2. "nodemon: not found" Error

**Symptom**: Container crashes with `sh: nodemon: not found`

**Cause**: `package.json` has `"start": "nodemon app.js"` but nodemon is a devDependency

**Fix**: Already handled automatically! The Dockerfile generator detects `nodemon` and replaces it with `node`.

### 3. Port Already Allocated

**Symptom**: `Bind for 0.0.0.0:{port} failed: port is already allocated`

**Cause**: Another container is already using that port

**Fix**:
```bash
# Find and remove conflicting container
docker rm -f $(docker ps -aq --filter "name=cp-")

# Or use different port range in .env
DATA_PLANE_PORT_RANGE_START=20000
DATA_PLANE_PORT_RANGE_END=20100
```

### 4. 502 Bad Gateway / 503 Service Unavailable

**Symptom**: Nginx returns 502/503 error

**Causes**:
1. Container crashed (check `docker ps -a`)
2. Container port mismatch (app listens on different port than expected)
3. App not binding to `0.0.0.0` (only binding to `localhost`)

**Fix**:
```bash
# Check if container is running
docker ps | grep {deploymentId}

# Check logs
docker logs cp-{deploymentId}

# Verify port binding
docker port cp-{deploymentId}
```