import { useState, useEffect } from "react";
import { io, Socket } from "socket.io-client";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

export interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

interface LogMeta {
  source: "redis" | "clickhouse";
  totalCount?: number;
}

interface UseLogsOptions {
  deploymentId: string;
  token?: string;
}

export function useLogs({ deploymentId, token }: UseLogsOptions) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [meta, setMeta] = useState<LogMeta | null>(null);
  const [isConnected, setIsConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [socket, setSocket] = useState<Socket | null>(null);

  // Initialize Socket.io connection
  useEffect(() => {
    if (!token) return;

    const newSocket = io(API, {
      transports: ["websocket", "polling"],
      auth: { token }
    });

    newSocket.on("connect", () => {
      console.log("[socket.io] connected");
      setIsConnected(true);
      setError(null);
    });

    newSocket.on("disconnect", () => {
      console.log("[socket.io] disconnected");
      setIsConnected(false);
    });

    newSocket.on("connect_error", (err) => {
      console.error("[socket.io] connection error:", err);
      setError("Connection error");
      setIsConnected(false);
    });

    setSocket(newSocket);

    return () => {
      newSocket.close();
    };
  }, [token]);

  // Subscribe to deployment logs
  useEffect(() => {
    if (!socket || !deploymentId) return;

    socket.emit("subscribe:logs", deploymentId, (response: { error?: string; success?: boolean }) => {
      if (response?.error) {
        console.error("[socket.io] subscribe error:", response.error);
        setError(response.error);
      }
    });

    socket.on("logs:history", (data: { logs: LogEntry[]; meta: LogMeta }) => {
      console.log("[socket.io] received history:", data.logs.length, "logs");
      setLogs(data.logs);
      setMeta(data.meta);
    });

    socket.on("logs:new", (log: LogEntry) => {
      setLogs((prev) => {
        const isDuplicate = prev.some(
          (l) => l.timestamp === log.timestamp && l.message === log.message
        );
        if (isDuplicate) return prev;
        return [...prev, log];
      });
    });

    return () => {
      socket.emit("unsubscribe:logs", deploymentId);
      socket.off("logs:history");
      socket.off("logs:new");
    };
  }, [socket, deploymentId]);

  const clearLogs = () => {
    setLogs([]);
  };

  return { logs, meta, isConnected, error, clearLogs };
}
