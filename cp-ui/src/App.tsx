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
import { LandingPage } from "./pages/LandingPage";
import logoSvg from "./assets/logo.svg";

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
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const handleLogin = async () => {
    setLoading(true);
    setError(null);
    try {
      await login();
      navigate("/projects");
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
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        background: "#0a0a0a",
        fontFamily:
          'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
        padding: "24px",
      }}
    >
      {/* Logo */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "12px",
          marginBottom: "40px",
          cursor: "pointer",
        }}
        onClick={() => window.location.reload()}
      >
        <img
          src={logoSvg}
          alt="Infra Pilot"
          style={{
            width: "32px",
            height: "32px",
          }}
        />
        <span
          style={{
            fontSize: "24px",
            fontWeight: 600,
            color: "#fff",
          }}
        >
          Infra Pilot
        </span>
      </div>

      {/* Card */}
      <div
        style={{
          background: "rgba(255,255,255,0.03)",
          border: "1px solid rgba(255,255,255,0.1)",
          borderRadius: "16px",
          padding: "40px",
          width: "100%",
          maxWidth: "400px",
        }}
      >
        <h1
          style={{
            margin: "0 0 8px 0",
            fontSize: "24px",
            fontWeight: 600,
            color: "#fff",
            textAlign: "center",
          }}
        >
          Sign in
        </h1>
        <p
          style={{
            margin: "0 0 32px 0",
            color: "#9ca3af",
            fontSize: "14px",
            textAlign: "center",
          }}
        >
          Enter your email and password to access your account
        </p>

        {error && (
          <div
            style={{
              background: "rgba(239,68,68,0.1)",
              color: "#ef4444",
              padding: "12px",
              borderRadius: "8px",
              marginBottom: "24px",
              fontSize: "14px",
              border: "1px solid rgba(239,68,68,0.2)",
            }}
          >
            {error}
          </div>
        )}

        {/* Email Field */}
        <div style={{ marginBottom: "20px" }}>
          <label
            style={{
              display: "block",
              fontSize: "14px",
              fontWeight: 500,
              color: "#fff",
              marginBottom: "8px",
            }}
          >
            Email
          </label>
          <input
            type="email"
            placeholder="john@doe.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            style={{
              width: "100%",
              padding: "12px 16px",
              background: "rgba(255,255,255,0.05)",
              border: "1px solid rgba(255,255,255,0.1)",
              borderRadius: "8px",
              fontSize: "14px",
              color: "#fff",
              outline: "none",
              boxSizing: "border-box",
              transition: "border-color 0.2s",
            }}
            onFocus={(e) =>
              (e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)")
            }
            onBlur={(e) =>
              (e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)")
            }
          />
        </div>

        {/* Password Field */}
        <div style={{ marginBottom: "8px" }}>
          <label
            style={{
              display: "block",
              fontSize: "14px",
              fontWeight: 500,
              color: "#fff",
              marginBottom: "8px",
            }}
          >
            Password
          </label>
          <input
            type="password"
            placeholder="Enter your password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            style={{
              width: "100%",
              padding: "12px 16px",
              background: "rgba(255,255,255,0.05)",
              border: "1px solid rgba(255,255,255,0.1)",
              borderRadius: "8px",
              fontSize: "14px",
              color: "#fff",
              outline: "none",
              boxSizing: "border-box",
              transition: "border-color 0.2s",
            }}
            onFocus={(e) =>
              (e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)")
            }
            onBlur={(e) =>
              (e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)")
            }
          />
        </div>

        {/* Forgot Password */}
        <div style={{ textAlign: "right", marginBottom: "24px" }}>
          <a
            href="#"
            style={{
              fontSize: "14px",
              color: "#9ca3af",
              textDecoration: "none",
              transition: "color 0.2s",
            }}
            onMouseEnter={(e) => (e.currentTarget.style.color = "#fff")}
            onMouseLeave={(e) => (e.currentTarget.style.color = "#9ca3af")}
          >
            Forgot password?
          </a>
        </div>

        {/* Sign In Button */}
        <button
          onClick={handleLogin}
          disabled={loading}
          style={{
            width: "100%",
            padding: "12px",
            background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
            color: "white",
            border: "none",
            borderRadius: "8px",
            fontSize: "14px",
            fontWeight: 500,
            cursor: loading ? "not-allowed" : "pointer",
            opacity: loading ? 0.7 : 1,
            transition: "box-shadow 0.2s, opacity 0.2s",
            marginBottom: "24px",
          }}
          onMouseEnter={(e) => {
            if (!loading) {
              e.currentTarget.style.boxShadow =
                "0 4px 20px rgba(139, 92, 246, 0.4)";
            }
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.boxShadow = "none";
          }}
        >
          {loading ? "Signing in..." : "Sign in"}
        </button>

        {/* Sign Up Link */}
        <p
          style={{
            margin: 0,
            fontSize: "14px",
            color: "#9ca3af",
            textAlign: "center",
          }}
        >
          Don't have an account?{" "}
          <a
            href="#"
            style={{
              color: "#fff",
              textDecoration: "none",
              fontWeight: 500,
            }}
          >
            Sign up
          </a>
        </p>
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

// Home route - shows landing page if not authenticated, projects if authenticated
function HomeRoute() {
  const token = localStorage.getItem("cp_token");
  if (token) {
    return <Navigate to="/projects" replace />;
  }
  return <LandingPage />;
}

// Projects list with logout
function ProjectsRoute() {
  return <ProjectsPage />;
}

// Canvas route with projectId from URL
function CanvasRoute() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();

  if (!projectId) {
    return <Navigate to="/projects" replace />;
  }

  return (
    <ProjectCanvasPage
      projectId={projectId}
      onBack={() => navigate("/projects")}
    />
  );
}

// Main app with router
function AppRoutes() {
  return (
    <>
      <MobileCheck />
      <Routes>
        <Route path="/" element={<HomeRoute />} />
        <Route path="/login" element={<LoginPage />} />
        <Route
          path="/projects"
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
