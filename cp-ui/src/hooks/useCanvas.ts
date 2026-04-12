import { useState, useEffect, useCallback } from "react";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

interface CanvasService {
  id: string;
  name: string;
  serviceType: string;
  positionX: number;
  positionY: number;
  deploymentCount: number;
}

interface Connection {
  id: string;
  sourceId: string;
  targetId: string;
  label?: string;
}

interface CanvasState {
  services: CanvasService[];
  connections: Connection[];
}

interface UseCanvasOptions {
  projectId: string;
  token?: string;
}

async function apiFetch<T>(path: string, opts: RequestInit = {}, token?: string): Promise<T> {
  const headers = new Headers(opts.headers);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (opts.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const res = await fetch(`${API}${path}`, { ...opts, headers });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status}: ${text}`);
  }
  return res.json();
}

export function useCanvas({ projectId, token }: UseCanvasOptions) {
  const [state, setState] = useState<CanvasState | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Fetch canvas state
  const loadCanvas = useCallback(async () => {
    if (!projectId || !token) return;

    setLoading(true);
    setError(null);

    try {
      const data = await apiFetch<CanvasState>(`/projects/${projectId}/canvas`, {}, token);
      setState(data);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, [projectId, token]);

  // Update single service position
  const updatePosition = useCallback(
    async (serviceId: string, positionX: number, positionY: number) => {
      if (!token) return;

      await apiFetch(
        `/services/${serviceId}/position`,
        {
          method: "POST",
          body: JSON.stringify({ positionX, positionY })
        },
        token
      );

      // Optimistically update local state
      setState(prev => {
        if (!prev) return prev;
        return {
          ...prev,
          services: prev.services.map(s =>
            s.id === serviceId ? { ...s, positionX, positionY } : s
          )
        };
      });
    },
    [token]
  );

  // Create connection between services
  const createConnection = useCallback(
    async (sourceId: string, targetId: string, label?: string) => {
      if (!token) return;

      const connection = await apiFetch<Connection>(
        "/services/connections",
        {
          method: "POST",
          body: JSON.stringify({ sourceId, targetId, label })
        },
        token
      );

      setState(prev => {
        if (!prev) return prev;
        return {
          ...prev,
          connections: [...prev.connections, connection]
        };
      });

      return connection;
    },
    [token]
  );

  // Delete connection
  const deleteConnection = useCallback(
    async (connectionId: string) => {
      if (!token) return;

      await apiFetch(
        `/services/connections/${connectionId}`,
        { method: "DELETE" },
        token
      );

      setState(prev => {
        if (!prev) return prev;
        return {
          ...prev,
          connections: prev.connections.filter(c => c.id !== connectionId)
        };
      });
    },
    [token]
  );

  // Run auto-layout
  const runAutoLayout = useCallback(async () => {
    if (!projectId || !token) return;

    const result = await apiFetch<{ services: Array<{ id: string; positionX: number; positionY: number }> }>(
      `/projects/${projectId}/canvas/auto-layout`,
      { method: "POST" },
      token
    );

    // Update local state with new positions
    setState(prev => {
      if (!prev) return prev;
      const positionMap = new Map(result.services.map(s => [s.id, s]));
      return {
        ...prev,
        services: prev.services.map(s => {
          const newPos = positionMap.get(s.id);
          return newPos ? { ...s, positionX: newPos.positionX, positionY: newPos.positionY } : s;
        })
      };
    });
  }, [projectId, token]);

  // Load on mount and when projectId/token changes
  useEffect(() => {
    loadCanvas();
  }, [loadCanvas]);

  return {
    state,
    loading,
    error,
    refresh: loadCanvas,
    updatePosition,
    createConnection,
    deleteConnection,
    runAutoLayout
  };
}
