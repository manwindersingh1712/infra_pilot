import { useEffect, useState } from "react";
import {
  BrowserRouter,
  Routes,
  Route,
  Navigate,
  useNavigate,
  useParams,
} from "react-router-dom";
import { ProjectsPage } from "./pages/ProjectsPage";
import { ProjectCanvasPage } from "./pages/ProjectCanvasPage";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

function useIsMobile() {
  const [isMobile, setIsMobile] = useState(false);

  useEffect(() => {
    const checkMobile = () => {
      setIsMobile(window.innerWidth < 768);
    };
    checkMobile();
    window.addEventListener("resize", checkMobile);
    return () => window.removeEventListener("resize", checkMobile);
  }, []);

  return isMobile;
}

async function apiFetch<T>(
  path: string,
  opts: RequestInit = {},
  token?: string
): Promise<T> {
  const headers = new Headers(opts.headers);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (opts.body != null && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const res = await fetch(`${API}${path}`, { ...opts, headers });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status}: ${text}`);
  }
  return res.json();
}

// Auth context
function useAuth() {
  const [token, setToken] = useState<string>(
    () => localStorage.getItem("cp_token") ?? ""
  );

  const login = async () => {
    const r = await apiFetch<{ token: string }>(`/dev/login`, {
      method: "POST",
    });
    localStorage.setItem("cp_token", r.token);
    setToken(r.token);
    return r.token;
  };

  const logout = () => {
    localStorage.removeItem("cp_token");
    setToken("");
  };

  return { token, login, logout };
}

// Mobile check component
function MobileCheck() {
  const isMobile = useIsMobile();

  if (isMobile) {
    return (
      <div
        style={{
          minHeight: "100vh",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          background: "#f3f4f6",
          padding: "24px",
          fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, Arial",
        }}
      >
        <div
          style={{
            background: "white",
            padding: "40px",
            borderRadius: "12px",
            boxShadow: "0 4px 12px rgba(0,0,0,0.1)",
            textAlign: "center",
            maxWidth: "400px",
          }}
        >
          <div style={{ fontSize: "48px", marginBottom: "16px" }}>💻</div>
          <h1 style={{ margin: "0 0 12px 0", fontSize: "24px", color: "#111827" }}>
            Desktop Required
          </h1>
          <p style={{ margin: 0, color: "#6b7280", lineHeight: 1.6 }}>
            Please access this website on a desktop computer for the best experience.
          </p>
        </div>
      </div>
    );
  }
  return null;
}

// Login page
function LoginPage() {
  const navigate = useNavigate();
  const { login } = useAuth();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleLogin = async () => {
    setLoading(true);
    setError(null);
    try {
      await login();
      navigate("/");
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div
      style={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        background: "#f5f5f5",
        fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, Arial",
      }}
    >
      <div
        style={{
          background: "white",
          padding: "40px",
          borderRadius: "12px",
          boxShadow: "0 4px 12px rgba(0,0,0,0.1)",
          textAlign: "center",
          minWidth: "300px",
        }}
      >
        <h1 style={{ margin: "0 0 8px 0", fontSize: "28px" }}>Infra Pilot</h1>
        <p style={{ margin: "0 0 24px 0", color: "#6b7280" }}>
          Infrastructure management made simple
        </p>

        {error && (
          <div
            style={{
              background: "#fee2e2",
              color: "#991b1b",
              padding: "12px",
              borderRadius: "6px",
              marginBottom: "16px",
              fontSize: "14px",
            }}
          >
            {error}
          </div>
        )}

        <button
          onClick={handleLogin}
          disabled={loading}
          style={{
            width: "100%",
            padding: "12px",
            background: "#3b82f6",
            color: "white",
            border: "none",
            borderRadius: "8px",
            fontSize: "16px",
            fontWeight: 500,
            cursor: loading ? "not-allowed" : "pointer",
            opacity: loading ? 0.6 : 1,
          }}
        >
          {loading ? "Logging in..." : "Login (Dev Mode)"}
        </button>
      </div>
    </div>
  );
}

// Protected route wrapper
function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const token = localStorage.getItem("cp_token");
  if (!token) {
    return <Navigate to="/login" replace />;
  }
  return <>{children}</>;
}

// Projects list with logout
function ProjectsRoute() {
  const navigate = useNavigate();
  const { logout } = useAuth();

  const handleLogout = () => {
    logout();
    navigate("/login");
  };

  return (
    <>
      <div
        style={{
          position: "fixed",
          top: 0,
          right: 0,
          padding: "12px 16px",
          zIndex: 1000,
        }}
      >
        <button
          onClick={handleLogout}
          style={{
            padding: "8px 16px",
            borderRadius: "6px",
            border: "1px solid #d1d5db",
            background: "white",
            cursor: "pointer",
            fontSize: "13px",
            color: "#6b7280",
          }}
        >
          Logout
        </button>
      </div>
      <ProjectsPage />
    </>
  );
}

// Canvas route with projectId from URL
function CanvasRoute() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();

  if (!projectId) {
    return <Navigate to="/" replace />;
  }

  return (
    <ProjectCanvasPage
      projectId={projectId}
      onBack={() => navigate("/")}
    />
  );
}

// Main app with router
function AppRoutes() {
  return (
    <>
      <MobileCheck />
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          path="/"
          element={
            <ProtectedRoute>
              <ProjectsRoute />
            </ProtectedRoute>
          }
        />
        <Route
          path="/projects/:projectId"
          element={
            <ProtectedRoute>
              <CanvasRoute />
            </ProtectedRoute>
          }
        />
      </Routes>
    </>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <AppRoutes />
    </BrowserRouter>
  );
}
