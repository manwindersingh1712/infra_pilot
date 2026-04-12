import { useState, useEffect, useRef } from "react";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

type ServiceType = "docker" | "nodejs" | "nextjs" | "react" | "mongodb" | "redis";
type Step = "select-type" | "database-options" | "github-type" | "service-form";

interface CreateServiceModalProps {
  projectId: string;
  isOpen: boolean;
  onClose: () => void;
  onCreated: () => void;
}

function getToken() {
  return localStorage.getItem("cp_token") ?? "";
}

async function apiFetch<T>(path: string, opts: RequestInit = {}): Promise<T> {
  const headers = new Headers(opts.headers);
  const token = getToken();
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

// Service category definitions
const serviceCategories = [
  {
    id: "github-repo",
    label: "GitHub Repository",
    icon: (
      <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor">
        <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0024 12c0-6.63-5.37-12-12-12z"/>
      </svg>
    ),
    description: "Deploy from a GitHub repository",
    serviceTypes: ["nodejs", "nextjs", "react"],
  },
  {
    id: "database",
    label: "Database",
    icon: (
      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <ellipse cx="12" cy="5" rx="9" ry="3"/>
        <path d="M3 5V19A9 3 0 0 0 21 19V5"/>
        <path d="M3 12A9 3 0 0 0 21 12"/>
      </svg>
    ),
    description: "MongoDB or Redis database",
    serviceTypes: ["mongodb", "redis"],
  },
];

// Database options with SVG icons
const databaseOptions = [
  {
    id: "mongodb",
    label: "MongoDB",
    icon: (
      <svg width="32" height="32" viewBox="0 0 32 32" fill="none">
        <path d="M16.62 6.64c-.25-.67-.53-1.3-.85-1.9-.32-.58-.67-1.13-1.06-1.62-.37-.48-.77-.9-1.2-1.25-.42-.35-.86-.62-1.31-.8l-.14-.05-.14.05c-.45.18-.89.45-1.31.8-.43.35-.83.77-1.2 1.25-.39.49-.74 1.04-1.06 1.62-.32.6-.6 1.23-.85 1.9-.5 1.35-.85 2.8-1.04 4.33-.19 1.52-.23 3.1-.12 4.72.11 1.62.37 3.26.78 4.9.41 1.63.97 3.23 1.67 4.77.35.77.73 1.5 1.15 2.2.42.69.87 1.33 1.35 1.92l.47.57.47-.57c.48-.59.93-1.23 1.35-1.92.42-.7.8-1.43 1.15-2.2.7-1.54 1.26-3.14 1.67-4.77.41-1.64.67-3.28.78-4.9.11-1.62.07-3.2-.12-4.72-.19-1.53-.54-2.98-1.04-4.33z" fill="#10AA50"/>
        <path d="M16.62 6.64c-.25-.67-.53-1.3-.85-1.9-.32-.58-.67-1.13-1.06-1.62-.37-.48-.77-.9-1.2-1.25-.42-.35-.86-.62-1.31-.8l-.14-.05v27.45l.47-.57c.48-.59.93-1.23 1.35-1.92.42-.7.8-1.43 1.15-2.2.7-1.54 1.26-3.14 1.67-4.77.41-1.64.67-3.28.78-4.9.11-1.62.07-3.2-.12-4.72-.19-1.53-.54-2.98-1.04-4.33z" fill="#B8C4C2"/>
        <path d="M12.96 29.88v-27.45l-.14.05c-.45.18-.89.45-1.31.8-.43.35-.83.77-1.2 1.25-.39.49-.74 1.04-1.06 1.62-.32.6-.6 1.23-.85 1.9-.5 1.35-.85 2.8-1.04 4.33-.19 1.52-.23 3.1-.12 4.72.11 1.62.37 3.26.78 4.9.41 1.63.97 3.23 1.67 4.77.35.77.73 1.5 1.15 2.2.42.69.87 1.33 1.35 1.92l.47.57v-2.38z" fill="#12924F"/>
      </svg>
    ),
    description: "Document NoSQL database"
  },
  {
    id: "redis",
    label: "Redis",
    icon: (
      <svg width="32" height="32" viewBox="0 0 32 32" fill="none">
        <path d="M28.3 20.6c-2.4 1.3-14.9 6.4-16.6 7.2-1.7.8-2.6.8-4-.2-1.4-1-11.7-4.9-13.9-5.8-1.2-.5-1.8-.9-1.8-1.3v-4c0 .4.6.8 1.8 1.3 2.2.9 12.5 4.8 13.9 5.8 1.4 1 2.3 1 4 .2 1.7-.8 14.2-5.9 16.6-7.2 1.2-.6 1.8-1 1.8-1.4v4c0 .4-.6.8-1.8 1.4z" fill="#A41E11"/>
        <path d="M28.3 16.6c-2.4 1.3-14.9 6.4-16.6 7.2-1.7.8-2.6.8-4-.2-1.4-1-11.7-4.9-13.9-5.8-2.2-.9-2.3-1.5 0-2.3 2.3-.8 13.6-4.1 15.5-4.7 1.9-.6 2.7-.6 4.7.2 2 1 12.3 4.4 14.4 5.2 2.1.9 2.2 1.5-.1 2.4z" fill="#D82C20"/>
        <path d="M28.3 12.4c-2.4 1.3-14.9 6.4-16.6 7.2-1.7.8-2.6.8-4-.2-1.4-1-11.7-4.9-13.9-5.8-1.2-.5-1.8-.9-1.8-1.3v-4c0 .4.6.8 1.8 1.3 2.2.9 12.5 4.8 13.9 5.8 1.4 1 2.3 1 4 .2 1.7-.8 14.2-5.9 16.6-7.2 1.2-.6 1.8-1 1.8-1.4v4c0 .4-.6.8-1.8 1.4z" fill="#A41E11"/>
        <path d="M28.3 8.4c-2.4 1.3-14.9 6.4-16.6 7.2-1.7.8-2.6.8-4-.2-1.4-1-11.7-4.9-13.9-5.8-2.2-.9-2.3-1.5 0-2.3 2.3-.8 13.6-4.1 15.5-4.7 1.9-.6 2.7-.6 4.7.2 2 1 12.3 4.4 14.4 5.2 2.1.9 2.2 1.5-.1 2.4z" fill="#D82C20"/>
        <path d="M18.1 8.5l-2.2.2-1.9 2.8-.4 3.2 1.5-.6.2-2.8 1-1.8 1.8-.6v-.4z" fill="#fff"/>
      </svg>
    ),
    description: "In-memory data store"
  },
];

// GitHub deployment types with SVG icons
const githubDeploymentTypes = [
  {
    id: "nodejs",
    label: "Node.js",
    icon: (
      <svg width="32" height="32" viewBox="0 0 32 32" fill="none">
        <path d="M16 0L1.5 8.5v15L16 32l14.5-8.5v-15L16 0z" fill="#539E43"/>
        <path d="M16 4L6 9.5v13L16 28l10-5.5v-13L16 4z" fill="#fff"/>
        <path d="M16 8l-6.5 3.75v7.5L16 23l6.5-3.75v-7.5L16 8z" fill="#333"/>
      </svg>
    ),
    description: "Auto-detect Node.js application"
  },
  {
    id: "nextjs",
    label: "Next.js",
    icon: (
      <svg width="32" height="32" viewBox="0 0 32 32" fill="none">
        <rect width="32" height="32" rx="6" fill="#000"/>
        <path d="M16 8v16" stroke="#fff" strokeWidth="2"/>
        <path d="M8 8l8 16" stroke="#fff" strokeWidth="2"/>
        <circle cx="22" cy="10" r="2" fill="#fff"/>
      </svg>
    ),
    description: "React framework with SSR"
  },
  {
    id: "react",
    label: "React",
    icon: (
      <svg width="32" height="32" viewBox="0 0 32 32" fill="none">
        <circle cx="16" cy="16" r="3" fill="#61DAFB"/>
        <ellipse cx="16" cy="16" rx="12" ry="5" stroke="#61DAFB" strokeWidth="1.5" fill="none"/>
        <ellipse cx="16" cy="16" rx="12" ry="5" stroke="#61DAFB" strokeWidth="1.5" fill="none" transform="rotate(60 16 16)"/>
        <ellipse cx="16" cy="16" rx="12" ry="5" stroke="#61DAFB" strokeWidth="1.5" fill="none" transform="rotate(120 16 16)"/>
      </svg>
    ),
    description: "React SPA application"
  },
  {
    id: "docker",
    label: "Docker",
    icon: (
      <svg width="32" height="32" viewBox="0 0 32 32" fill="none">
        <rect width="32" height="32" rx="6" fill="#2496ED"/>
        <path d="M24 14.5c-.5-.3-1.1-.4-1.7-.2-.2-.8-.7-1.5-1.3-2l-.2-.2-.3.2c-.3.4-.4.9-.5 1.4-.2.6 0 1.2.4 1.7-.2.1-.5.2-.7.3h-8.2v.5c0 2.4 1 4.7 2.8 6.3 1.4 1.2 3.2 1.9 5.1 1.9h.7c1.7.1 3.4-.5 4.7-1.5 1.2-.9 2-2.1 2.3-3.5.7.1 1.4-.1 2-.4.5-.3.9-.7 1.1-1.2l.1-.3-.2-.2z" fill="#fff"/>
        <path d="M16 12h-1.5v1.5H16V12zm-2 0h-1.5v1.5H14V12zm-2 0h-1.5v1.5H12V12zm-2 0H8.5v1.5H10V12zm-2 2H6.5v1.5H8v-1.5zm2 0h-1.5v1.5H10v-1.5zm2 0h-1.5v1.5H12v-1.5zm2 0h-1.5v1.5H14v-1.5zm2 0h-1.5v1.5H16v-1.5z" fill="#fff"/>
      </svg>
    ),
    description: "Custom Dockerfile"
  },
];

// Quick suggestions
const quickSuggestions = [
  { id: "quick-1", label: "Create a full-stack app with database", icon: "✨", action: () => "github-repo" as const },
  { id: "quick-2", label: "Deploy Redis and MongoDB", icon: "✨", action: () => "database" as const },
];

export function CreateServiceModal({ projectId, isOpen, onClose, onCreated }: CreateServiceModalProps) {
  const [step, setStep] = useState<Step>("select-type");
  const [selectedCategory, setSelectedCategory] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [serviceType, setServiceType] = useState<ServiceType>("docker");
  const [repoUrl, setRepoUrl] = useState("");
  const [branch, setBranch] = useState("main");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  // Auto-focus input when modal opens or step changes to select-type
  useEffect(() => {
    if (isOpen && step === "select-type" && inputRef.current) {
      setTimeout(() => inputRef.current?.focus(), 50);
    }
  }, [isOpen, step]);

  // Handle Escape key
  useEffect(() => {
    function handleEscape(e: KeyboardEvent) {
      if (e.key === "Escape" && isOpen) {
        handleClose();
      }
    }
    document.addEventListener("keydown", handleEscape);
    return () => document.removeEventListener("keydown", handleEscape);
  }, [isOpen]);

  if (!isOpen) return null;

  const needsRepo = serviceType === "docker" || serviceType === "nodejs" || serviceType === "nextjs" || serviceType === "react";

  function resetForm() {
    setStep("select-type");
    setSelectedCategory(null);
    setName("");
    setServiceType("docker");
    setRepoUrl("");
    setBranch("main");
    setError(null);
  }

  function handleClose() {
    resetForm();
    onClose();
  }

  function handleCategorySelect(categoryId: string) {
    setSelectedCategory(categoryId);
    if (categoryId === "database") {
      setStep("database-options");
    } else if (categoryId === "github-repo") {
      setStep("github-type");
    } else if (categoryId === "docker") {
      setServiceType("docker");
      setStep("service-form");
    }
  }

  function handleGithubTypeSelect(type: ServiceType) {
    setServiceType(type);
    setStep("service-form");
  }

  function handleDatabaseSelect(dbType: ServiceType) {
    setServiceType(dbType);
    setStep("service-form");
  }

  function handleBack() {
    if (step === "database-options") {
      setStep("select-type");
      setSelectedCategory(null);
    } else if (step === "github-type") {
      setStep("select-type");
      setSelectedCategory(null);
    } else if (step === "service-form") {
      if (selectedCategory === "database") {
        setStep("database-options");
      } else if (selectedCategory === "github-repo") {
        setStep("github-type");
      } else {
        setStep("select-type");
        setSelectedCategory(null);
      }
    }
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;

    setLoading(true);
    setError(null);

    try {
      const body: any = {
        projectId,
        name,
        serviceType
      };

      if (needsRepo) {
        body.repoUrl = repoUrl;
        body.branch = branch;
      }

      // Create service
      const service = await apiFetch<{ id: string }>("/services", {
        method: "POST",
        body: JSON.stringify(body)
      });

      // Auto-deploy the service
      await apiFetch(`/services/${service.id}/deploy`, {
        method: "POST",
        body: JSON.stringify({ commitSha: "HEAD" })
      });

      resetForm();
      onCreated();
      onClose();
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }

  const inputStyle = {
    width: "100%",
    padding: "10px 14px",
    borderRadius: "8px",
    border: "1px solid rgba(255,255,255,0.1)",
    fontSize: "13px",
    boxSizing: "border-box" as const,
    background: "rgba(255,255,255,0.05)",
    color: "#fff",
    outline: "none",
    transition: "border-color 0.2s",
  };

  const labelStyle = {
    display: "block",
    fontSize: "12px",
    fontWeight: 500,
    marginBottom: "6px",
    color: "#e5e7eb",
  };

  return (
    <div style={{
      position: "fixed",
      inset: 0,
      background: "transparent",
      display: "flex",
      alignItems: "flex-start",
      justifyContent: "center",
      paddingTop: "120px",
      zIndex: 1000,
      fontFamily: 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
    }} onClick={handleClose}>
      <div style={{
        background: "#1a1a2e",
        borderRadius: "12px",
        width: "100%",
        maxWidth: "420px",
        maxHeight: "70vh",
        overflow: "auto",
        border: "1px solid rgba(255,255,255,0.1)",
        boxShadow: "0 20px 40px rgba(0,0,0,0.4), 0 0 0 1px rgba(139,92,246,0.2)",
      }} onClick={(e) => e.stopPropagation()}>
        {/* Header with back button - hidden on select-type step */}
        {step !== "select-type" && (
          <div style={{
            padding: "12px 16px",
            borderBottom: "1px solid rgba(255,255,255,0.08)",
            display: "flex",
            alignItems: "center",
            gap: "10px",
          }}>
            <button
              onClick={handleBack}
              style={{
                background: "none",
                border: "none",
                color: "#9ca3af",
                cursor: "pointer",
                fontSize: "16px",
                padding: "4px",
                borderRadius: "6px",
                transition: "background 0.2s, color 0.2s",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.background = "rgba(255,255,255,0.05)";
                e.currentTarget.style.color = "#fff";
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.background = "transparent";
                e.currentTarget.style.color = "#9ca3af";
              }}
            >
              ←
            </button>
            <h2 style={{
              margin: 0,
              fontSize: "14px",
              fontWeight: 600,
              color: "#fff",
              flex: 1,
            }}>
              {step === "database-options" && "Select Database"}
              {step === "github-type" && "Select Type"}
              {step === "service-form" && (serviceType === "mongodb" || serviceType === "redis" ? "Configure Database" : "Configure")}
            </h2>
            <button
              onClick={handleClose}
              style={{
                background: "none",
                border: "none",
                fontSize: "18px",
                cursor: "pointer",
                color: "#9ca3af",
                lineHeight: 1,
                padding: "4px",
                borderRadius: "6px",
                transition: "background 0.2s, color 0.2s",
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.background = "rgba(255,255,255,0.05)";
                e.currentTarget.style.color = "#fff";
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.background = "transparent";
                e.currentTarget.style.color = "#9ca3af";
              }}
            >
              ×
            </button>
          </div>
        )}

        {/* Step 1: Select Type */}
        {step === "select-type" && (
          <div style={{ padding: "16px" }}>
            {/* Search Input */}
            <div style={{ marginBottom: "16px" }}>
              <input
                ref={inputRef}
                type="text"
                placeholder="What would you like to create?"
                style={{
                  ...inputStyle,
                  fontSize: "14px",
                  padding: "12px 14px",
                }}
                onFocus={(e) => {
                  e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)";
                }}
                onBlur={(e) => {
                  e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
                }}
              />
            </div>

            {/* Quick Suggestions */}
            <div style={{ marginBottom: "16px" }}>
              {quickSuggestions.map((suggestion) => (
                <button
                  key={suggestion.id}
                  onClick={() => handleCategorySelect(suggestion.action())}
                  style={{
                    width: "100%",
                    padding: "10px 14px",
                    marginBottom: "6px",
                    background: "rgba(255,255,255,0.03)",
                    border: "1px solid rgba(255,255,255,0.06)",
                    borderRadius: "8px",
                    display: "flex",
                    alignItems: "center",
                    gap: "10px",
                    cursor: "pointer",
                    transition: "background 0.2s, border-color 0.2s",
                    textAlign: "left",
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "rgba(139,92,246,0.1)";
                    e.currentTarget.style.borderColor = "rgba(139,92,246,0.2)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "rgba(255,255,255,0.03)";
                    e.currentTarget.style.borderColor = "rgba(255,255,255,0.06)";
                  }}
                >
                  <span style={{ fontSize: "14px" }}>{suggestion.icon}</span>
                  <span style={{ color: "#e5e7eb", fontSize: "13px", fontWeight: 400 }}>
                    {suggestion.label}
                  </span>
                </button>
              ))}
            </div>

            {/* Divider */}
            <div style={{
              height: "1px",
              background: "rgba(255,255,255,0.06)",
              marginBottom: "12px",
            }} />

            {/* Categories */}
            <div>
              {serviceCategories.map((category) => (
                <button
                  key={category.id}
                  onClick={() => handleCategorySelect(category.id)}
                  style={{
                    width: "100%",
                    padding: "10px 12px",
                    marginBottom: "2px",
                    background: "transparent",
                    border: "none",
                    borderRadius: "6px",
                    display: "flex",
                    alignItems: "center",
                    gap: "12px",
                    cursor: "pointer",
                    transition: "background 0.2s",
                    textAlign: "left",
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "rgba(255,255,255,0.05)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "transparent";
                  }}
                >
                  <span style={{ color: "#9ca3af", display: "flex", alignItems: "center" }}>
                    {category.icon}
                  </span>
                  <div style={{ flex: 1 }}>
                    <div style={{ color: "#e5e7eb", fontSize: "13px", fontWeight: 400 }}>
                      {category.label}
                    </div>
                  </div>
                  <span style={{ color: "#6b7280", fontSize: "14px" }}>›</span>
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Step 2: Database Options */}
        {step === "database-options" && (
          <div style={{ padding: "12px" }}>
            <div>
              {databaseOptions.map((db) => (
                <button
                  key={db.id}
                  onClick={() => handleDatabaseSelect(db.id as ServiceType)}
                  style={{
                    width: "100%",
                    padding: "10px 12px",
                    marginBottom: "4px",
                    background: "transparent",
                    border: "none",
                    borderRadius: "6px",
                    display: "flex",
                    alignItems: "center",
                    gap: "12px",
                    cursor: "pointer",
                    transition: "background 0.2s",
                    textAlign: "left",
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "rgba(255,255,255,0.05)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "transparent";
                  }}
                >
                  <span style={{ display: "flex", alignItems: "center" }}>{db.icon}</span>
                  <div style={{ flex: 1 }}>
                    <div style={{ color: "#e5e7eb", fontSize: "13px" }}>
                      {db.label}
                    </div>
                  </div>
                  <span style={{ color: "#6b7280", fontSize: "14px" }}>›</span>
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Step 2b: GitHub Deployment Type */}
        {step === "github-type" && (
          <div style={{ padding: "12px" }}>
            <div>
              {githubDeploymentTypes.map((type) => (
                <button
                  key={type.id}
                  onClick={() => handleGithubTypeSelect(type.id as ServiceType)}
                  style={{
                    width: "100%",
                    padding: "10px 12px",
                    marginBottom: "4px",
                    background: "transparent",
                    border: "none",
                    borderRadius: "6px",
                    display: "flex",
                    alignItems: "center",
                    gap: "12px",
                    cursor: "pointer",
                    transition: "background 0.2s",
                    textAlign: "left",
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "rgba(255,255,255,0.05)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "transparent";
                  }}
                >
                  <span style={{ display: "flex", alignItems: "center" }}>{type.icon}</span>
                  <div style={{ flex: 1 }}>
                    <div style={{ color: "#e5e7eb", fontSize: "13px" }}>
                      {type.label}
                    </div>
                  </div>
                  <span style={{ color: "#6b7280", fontSize: "14px" }}>›</span>
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Step 3: Service Form */}
        {step === "service-form" && (
          <form onSubmit={handleSubmit} style={{ padding: "20px" }}>
            {error && (
              <div style={{
                background: "rgba(239,68,68,0.1)",
                color: "#ef4444",
                padding: "12px 16px",
                borderRadius: "8px",
                marginBottom: "20px",
                fontSize: "14px",
                border: "1px solid rgba(239,68,68,0.2)",
              }}>
                {error}
              </div>
            )}

            {/* Service Type Display */}
            <div style={{
              display: "flex",
              alignItems: "center",
              gap: "12px",
              padding: "12px 16px",
              background: "rgba(139,92,246,0.1)",
              borderRadius: "10px",
              marginBottom: "20px",
              border: "1px solid rgba(139,92,246,0.2)",
            }}>
              <span style={{ display: "flex", alignItems: "center" }}>
                {serviceType === "mongodb" ? (
                  <svg width="28" height="28" viewBox="0 0 32 32" fill="none">
                    <path d="M16.62 6.64c-.25-.67-.53-1.3-.85-1.9-.32-.58-.67-1.13-1.06-1.62-.37-.48-.77-.9-1.2-1.25-.42-.35-.86-.62-1.31-.8l-.14-.05-.14.05c-.45.18-.89.45-1.31.8-.43.35-.83.77-1.2 1.25-.39.49-.74 1.04-1.06 1.62-.32.6-.6 1.23-.85 1.9-.5 1.35-.85 2.8-1.04 4.33-.19 1.52-.23 3.1-.12 4.72.11 1.62.37 3.26.78 4.9.41 1.63.97 3.23 1.67 4.77.35.77.73 1.5 1.15 2.2.42.69.87 1.33 1.35 1.92l.47.57.47-.57c.48-.59.93-1.23 1.35-1.92.42-.7.8-1.43 1.15-2.2.7-1.54 1.26-3.14 1.67-4.77.41-1.64.67-3.28.78-4.9.11-1.62.07-3.2-.12-4.72-.19-1.53-.54-2.98-1.04-4.33z" fill="#10AA50"/>
                    <path d="M16.62 6.64c-.25-.67-.53-1.3-.85-1.9-.32-.58-.67-1.13-1.06-1.62-.37-.48-.77-.9-1.2-1.25-.42-.35-.86-.62-1.31-.8l-.14-.05v27.45l.47-.57c.48-.59.93-1.23 1.35-1.92.42-.7.8-1.43 1.15-2.2.7-1.54 1.26-3.14 1.67-4.77.41-1.64.67-3.28.78-4.9.11-1.62.07-3.2-.12-4.72-.19-1.53-.54-2.98-1.04-4.33z" fill="#B8C4C2"/>
                    <path d="M12.96 29.88v-27.45l-.14.05c-.45.18-.89.45-1.31.8-.43.35-.83.77-1.2 1.25-.39.49-.74 1.04-1.06 1.62-.32.6-.6 1.23-.85 1.9-.5 1.35-.85 2.8-1.04 4.33-.19 1.52-.23 3.1-.12 4.72.11 1.62.37 3.26.78 4.9.41 1.63.97 3.23 1.67 4.77.35.77.73 1.5 1.15 2.2.42.69.87 1.33 1.35 1.92l.47.57v-2.38z" fill="#12924F"/>
                  </svg>
                ) : serviceType === "redis" ? (
                  <svg width="28" height="28" viewBox="0 0 32 32" fill="none">
                    <path d="M28.3 20.6c-2.4 1.3-14.9 6.4-16.6 7.2-1.7.8-2.6.8-4-.2-1.4-1-11.7-4.9-13.9-5.8-1.2-.5-1.8-.9-1.8-1.3v-4c0 .4.6.8 1.8 1.3 2.2.9 12.5 4.8 13.9 5.8 1.4 1 2.3 1 4 .2 1.7-.8 14.2-5.9 16.6-7.2 1.2-.6 1.8-1 1.8-1.4v4c0 .4-.6.8-1.8 1.4z" fill="#A41E11"/>
                    <path d="M28.3 16.6c-2.4 1.3-14.9 6.4-16.6 7.2-1.7.8-2.6.8-4-.2-1.4-1-11.7-4.9-13.9-5.8-2.2-.9-2.3-1.5 0-2.3 2.3-.8 13.6-4.1 15.5-4.7 1.9-.6 2.7-.6 4.7.2 2 1 12.3 4.4 14.4 5.2 2.1.9 2.2 1.5-.1 2.4z" fill="#D82C20"/>
                    <path d="M28.3 12.4c-2.4 1.3-14.9 6.4-16.6 7.2-1.7.8-2.6.8-4-.2-1.4-1-11.7-4.9-13.9-5.8-1.2-.5-1.8-.9-1.8-1.3v-4c0 .4.6.8 1.8 1.3 2.2.9 12.5 4.8 13.9 5.8 1.4 1 2.3 1 4 .2 1.7-.8 14.2-5.9 16.6-7.2 1.2-.6 1.8-1 1.8-1.4v4c0 .4-.6.8-1.8 1.4z" fill="#A41E11"/>
                    <path d="M28.3 8.4c-2.4 1.3-14.9 6.4-16.6 7.2-1.7.8-2.6.8-4-.2-1.4-1-11.7-4.9-13.9-5.8-2.2-.9-2.3-1.5 0-2.3 2.3-.8 13.6-4.1 15.5-4.7 1.9-.6 2.7-.6 4.7.2 2 1 12.3 4.4 14.4 5.2 2.1.9 2.2 1.5-.1 2.4z" fill="#D82C20"/>
                    <path d="M18.1 8.5l-2.2.2-1.9 2.8-.4 3.2 1.5-.6.2-2.8 1-1.8 1.8-.6v-.4z" fill="#fff"/>
                  </svg>
                ) : serviceType === "nextjs" ? (
                  <svg width="28" height="28" viewBox="0 0 32 32" fill="none">
                    <rect width="32" height="32" rx="6" fill="#fff"/>
                    <path d="M16 8v16" stroke="#000" strokeWidth="2"/>
                    <path d="M8 8l8 16" stroke="#000" strokeWidth="2"/>
                    <circle cx="22" cy="10" r="2" fill="#000"/>
                  </svg>
                ) : serviceType === "react" ? (
                  <svg width="28" height="28" viewBox="0 0 32 32" fill="none">
                    <circle cx="16" cy="16" r="3" fill="#61DAFB"/>
                    <ellipse cx="16" cy="16" rx="12" ry="5" stroke="#61DAFB" strokeWidth="1.5" fill="none"/>
                    <ellipse cx="16" cy="16" rx="12" ry="5" stroke="#61DAFB" strokeWidth="1.5" fill="none" transform="rotate(60 16 16)"/>
                    <ellipse cx="16" cy="16" rx="12" ry="5" stroke="#61DAFB" strokeWidth="1.5" fill="none" transform="rotate(120 16 16)"/>
                  </svg>
                ) : serviceType === "docker" ? (
                  <svg width="28" height="28" viewBox="0 0 32 32" fill="none">
                    <rect width="32" height="32" rx="6" fill="#2496ED"/>
                    <path d="M24 14.5c-.5-.3-1.1-.4-1.7-.2-.2-.8-.7-1.5-1.3-2l-.2-.2-.3.2c-.3.4-.4.9-.5 1.4-.2.6 0 1.2.4 1.7-.2.1-.5.2-.7.3h-8.2v.5c0 2.4 1 4.7 2.8 6.3 1.4 1.2 3.2 1.9 5.1 1.9h.7c1.7.1 3.4-.5 4.7-1.5 1.2-.9 2-2.1 2.3-3.5.7.1 1.4-.1 2-.4.5-.3.9-.7 1.1-1.2l.1-.3-.2-.2z" fill="#fff"/>
                    <path d="M16 12h-1.5v1.5H16V12zm-2 0h-1.5v1.5H14V12zm-2 0h-1.5v1.5H12V12zm-2 0H8.5v1.5H10V12zm-2 2H6.5v1.5H8v-1.5zm2 0h-1.5v1.5H10v-1.5zm2 0h-1.5v1.5H12v-1.5zm2 0h-1.5v1.5H14v-1.5zm2 0h-1.5v1.5H16v-1.5z" fill="#fff"/>
                  </svg>
                ) : (
                  <svg width="28" height="28" viewBox="0 0 32 32" fill="none">
                    <path d="M16 0L1.5 8.5v15L16 32l14.5-8.5v-15L16 0z" fill="#539E43"/>
                    <path d="M16 4L6 9.5v13L16 28l10-5.5v-13L16 4z" fill="#fff"/>
                    <path d="M16 8l-6.5 3.75v7.5L16 23l6.5-3.75v-7.5L16 8z" fill="#333"/>
                  </svg>
                )}
              </span>
              <div>
                <div style={{ color: "#fff", fontSize: "14px", fontWeight: 600, textTransform: "capitalize" }}>
                  {serviceType === "nodejs" ? "Node.js" : serviceType}
                </div>
                <div style={{ color: "#9ca3af", fontSize: "12px" }}>
                  {serviceType === "mongodb" || serviceType === "redis"
                    ? "Managed database service"
                    : needsRepo
                      ? "GitHub repository deployment"
                      : "Container deployment"}
                </div>
              </div>
            </div>

            <div style={{ marginBottom: "20px" }}>
              <label style={labelStyle}>
                Service Name *
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={serviceType === "mongodb" ? "my-mongodb" : serviceType === "redis" ? "my-redis" : "my-service"}
                required
                style={inputStyle}
                onFocus={(e) => {
                  e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)";
                }}
                onBlur={(e) => {
                  e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
                }}
              />
            </div>

            {needsRepo && (
              <>
                <div style={{ marginBottom: "20px" }}>
                  <label style={labelStyle}>
                    GitHub Repository URL *
                  </label>
                  <input
                    type="text"
                    value={repoUrl}
                    onChange={(e) => setRepoUrl(e.target.value)}
                    placeholder="https://github.com/username/repo"
                    required={needsRepo}
                    style={inputStyle}
                    onFocus={(e) => {
                      e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)";
                    }}
                    onBlur={(e) => {
                      e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
                    }}
                  />
                </div>

                <div style={{ marginBottom: "24px" }}>
                  <label style={labelStyle}>
                    Branch
                  </label>
                  <input
                    type="text"
                    value={branch}
                    onChange={(e) => setBranch(e.target.value)}
                    placeholder="main"
                    style={inputStyle}
                    onFocus={(e) => {
                      e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)";
                    }}
                    onBlur={(e) => {
                      e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
                    }}
                  />
                </div>
              </>
            )}

            <div style={{ display: "flex", gap: "12px", justifyContent: "flex-end" }}>
              <button
                type="button"
                onClick={handleClose}
                style={{
                  padding: "10px 20px",
                  borderRadius: "8px",
                  border: "1px solid rgba(255,255,255,0.1)",
                  background: "rgba(255,255,255,0.05)",
                  color: "#e5e7eb",
                  cursor: "pointer",
                  fontSize: "14px",
                  fontWeight: 500,
                  transition: "background 0.2s",
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.background = "rgba(255,255,255,0.1)";
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.background = "rgba(255,255,255,0.05)";
                }}
              >
                Cancel
              </button>
              <button
                type="submit"
                disabled={loading || !name.trim() || (needsRepo && !repoUrl.trim())}
                style={{
                  padding: "10px 20px",
                  borderRadius: "8px",
                  border: "none",
                  background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
                  color: "white",
                  cursor: loading ? "not-allowed" : "pointer",
                  opacity: loading ? 0.6 : 1,
                  fontSize: "14px",
                  fontWeight: 500,
                  transition: "box-shadow 0.2s, opacity 0.2s",
                }}
                onMouseEnter={(e) => {
                  if (!loading) {
                    e.currentTarget.style.boxShadow = "0 4px 20px rgba(139, 92, 246, 0.4)";
                  }
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.boxShadow = "none";
                }}
              >
                {loading ? "Creating..." : serviceType === "mongodb" || serviceType === "redis" ? "Create Database" : "Create Service"}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
