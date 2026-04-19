import { useNavigate } from "react-router-dom";
import { useEffect, useRef, useState } from "react";
import { animate, inView, hover, stagger } from "motion";
import logoSvg from "../assets/logo.svg";

export function LandingPage() {
  const navigate = useNavigate();
  const [isLoaded, setIsLoaded] = useState(false);

  // Refs for animated elements
  const heroBadgeRef = useRef<HTMLDivElement>(null);
  const heroTitleRef = useRef<HTMLHeadingElement>(null);
  const heroSubtitleRef = useRef<HTMLParagraphElement>(null);
  const heroButtonsRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<HTMLDivElement>(null);
  const featuresRef = useRef<HTMLDivElement>(null);
  const stepsRef = useRef<HTMLDivElement>(null);
  const glowRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setIsLoaded(true);

    // Hero entrance animations - staggered fade in + slide up
    const heroElements = [
      heroBadgeRef.current,
      heroTitleRef.current,
      heroSubtitleRef.current,
      heroButtonsRef.current,
      terminalRef.current,
    ].filter(Boolean);

    heroElements.forEach((el, i) => {
      if (el) {
        animate(
          el,
          { opacity: [0, 1], y: [30, 0] },
          { duration: 0.6, delay: i * 0.1, easing: [0.25, 0.46, 0.45, 0.94] }
        );
      }
    });

    // Background glow pulse animation
    if (glowRef.current) {
      animate(
        glowRef.current,
        { scale: [1, 1.15, 1], opacity: [0.8, 1, 0.8] },
        { duration: 5, repeat: Infinity, easing: "easeInOut" }
      );
    }

    // Terminal glow effect
    if (terminalRef.current) {
      hover(terminalRef.current, (element) => {
        animate(
          element,
          { boxShadow: ["0 0 0 rgba(139,92,246,0)", "0 0 30px rgba(139,92,246,0.3)", "0 0 0 rgba(139,92,246,0)"] },
          { duration: 0.6 }
        );
        return () => {
          animate(element, { boxShadow: "0 0 0 rgba(139,92,246,0)" }, { duration: 0.3 });
        };
      });
    }

    // Feature cards scroll-triggered animation with stagger
    if (featuresRef.current) {
      const cards = featuresRef.current.querySelectorAll("[data-animate]");
      inView(
        featuresRef.current,
        () => {
          animate(
            cards,
            { opacity: [0, 1], y: [40, 0], scale: [0.95, 1] },
            { duration: 0.5, delay: stagger(0.08), easing: [0.25, 0.46, 0.45, 0.94] }
          );
        },
        { margin: "-100px" }
      );
    }

    // Steps scroll-triggered animation
    if (stepsRef.current) {
      const stepCards = stepsRef.current.querySelectorAll("[data-step]");
      inView(
        stepsRef.current,
        () => {
          animate(
            stepCards,
            { opacity: [0, 1], x: [-30, 0] },
            { duration: 0.5, delay: stagger(0.15), easing: [0.25, 0.46, 0.45, 0.94] }
          );
        },
        { margin: "-100px" }
      );
    }

    // Button hover effects
    const buttons = document.querySelectorAll("[data-hover]");
    buttons.forEach((button) => {
      hover(button, (element) => {
        animate(element, { scale: 1.05 }, { duration: 0.2 });
        return () => {
          animate(element, { scale: 1 }, { duration: 0.2 });
        };
      });
    });
  }, []);

  const scrollToSection = (id: string) => {
    const element = document.getElementById(id);
    if (element) {
      element.scrollIntoView({ behavior: "smooth" });
    }
  };

  return (
    <div
      style={{
        minHeight: "100vh",
        background: "#0a0a0a",
        color: "#fff",
        fontFamily:
          'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      }}
    >
      {/* Navbar */}
      <nav
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          padding: "16px 48px",
          borderBottom: "1px solid rgba(255,255,255,0.1)",
          opacity: isLoaded ? 1 : 0,
          transition: "opacity 0.5s ease",
        }}
      >
        <div
          style={{ display: "flex", alignItems: "center", gap: "8px", cursor: "pointer" }}
          onClick={() => navigate("/")}
        >
          <img
            src={logoSvg}
            alt="Infra Pilot"
            style={{
              width: "28px",
              height: "28px",
            }}
          />
          <span style={{ fontSize: "18px", fontWeight: 600 }}>Infra Pilot</span>
        </div>

        <div style={{ display: "flex", gap: "32px" }}>
          <button
            onClick={() => scrollToSection("features")}
            style={{
              background: "none",
              border: "none",
              color: "#9ca3af",
              cursor: "pointer",
              fontSize: "14px",
              padding: "8px 0",
              transition: "color 0.2s",
            }}
            onMouseEnter={(e) => (e.currentTarget.style.color = "#fff")}
            onMouseLeave={(e) => (e.currentTarget.style.color = "#9ca3af")}
          >
            Features
          </button>
          <button
            onClick={() => scrollToSection("how-it-works")}
            style={{
              background: "none",
              border: "none",
              color: "#9ca3af",
              cursor: "pointer",
              fontSize: "14px",
              padding: "8px 0",
              transition: "color 0.2s",
            }}
            onMouseEnter={(e) => (e.currentTarget.style.color = "#fff")}
            onMouseLeave={(e) => (e.currentTarget.style.color = "#9ca3af")}
          >
            How it Works
          </button>
        </div>

        <div style={{ display: "flex", gap: "12px", alignItems: "center" }}>
          <button
            onClick={() => navigate("/login")}
            style={{
              background: "none",
              border: "none",
              color: "#fff",
              cursor: "pointer",
              fontSize: "14px",
              padding: "8px 16px",
              transition: "opacity 0.2s",
            }}
            onMouseEnter={(e) => (e.currentTarget.style.opacity = "0.8")}
            onMouseLeave={(e) => (e.currentTarget.style.opacity = "1")}
          >
            Sign In
          </button>
          <button
            data-hover
            onClick={() => navigate("/login")}
            style={{
              background: "linear-gradient(135deg, #8b5cf6, #6366f1)",
              border: "none",
              color: "#fff",
              cursor: "pointer",
              fontSize: "14px",
              padding: "10px 20px",
              borderRadius: "8px",
              fontWeight: 500,
              transition: "box-shadow 0.3s",
            }}
            onMouseEnter={(e) =>
              (e.currentTarget.style.boxShadow =
                "0 4px 20px rgba(139, 92, 246, 0.4)")
            }
            onMouseLeave={(e) =>
              (e.currentTarget.style.boxShadow = "none")
            }
          >
            Get Started
          </button>
        </div>
      </nav>

      {/* Hero Section */}
      <section
        style={{
          padding: "60px 48px",
          textAlign: "center",
          position: "relative",
          overflow: "hidden",
        }}
      >
        {/* Background glow effect */}
        <div
          ref={glowRef}
          style={{
            position: "absolute",
            top: "35%",
            left: "33%",
            transform: "translate(-50%, -50%)",
            width: "500px",
            height: "250px",
            background:
              "radial-gradient(ellipse at center, rgba(139,92,246,0.6) 0%, rgba(124,58,237,0.4) 25%, rgba(59,130,246,0.2) 50%, transparent 70%)",
            pointerEvents: "none",
            filter: "blur(40px)",
            opacity: 1,
            zIndex: 0,
          }}
        />

        <div
          ref={heroBadgeRef}
          style={{
            display: "inline-flex",
            alignItems: "center",
            gap: "8px",
            padding: "8px 16px",
            background: "rgba(139,92,246,0.1)",
            border: "1px solid rgba(139,92,246,0.3)",
            borderRadius: "20px",
            marginBottom: "32px",
            opacity: 0,
            position: "relative",
            zIndex: 1,
          }}
        >
          <span>⚡</span>
          <span style={{ fontSize: "14px", color: "#c4b5fd" }}>
            Visual infrastructure management
          </span>
        </div>

        <h1
          ref={heroTitleRef}
          style={{
            fontSize: "56px",
            fontWeight: 700,
            margin: "0 0 24px 0",
            lineHeight: 1.1,
            opacity: 0,
            position: "relative",
            zIndex: 1,
          }}
        >
          Visualize your
          <br />
          <span
            style={{
              background: "linear-gradient(135deg, #8b5cf6, #3b82f6, #06b6d4)",
              WebkitBackgroundClip: "text",
              WebkitTextFillColor: "transparent",
              backgroundClip: "text",
            }}
          >
            infrastructure
          </span>
        </h1>

        <p
          ref={heroSubtitleRef}
          style={{
            fontSize: "18px",
            color: "#9ca3af",
            maxWidth: "600px",
            margin: "0 auto 40px",
            lineHeight: 1.6,
            opacity: 0,
            position: "relative",
            zIndex: 1,
          }}
        >
          Design, deploy, and manage your services on an interactive 2D canvas.
          Visualize connections, monitor deployments, and configure everything
          in one place.
        </p>

        <div
          ref={heroButtonsRef}
          style={{
            display: "flex",
            gap: "16px",
            justifyContent: "center",
            marginBottom: "40px",
            opacity: 0,
            position: "relative",
            zIndex: 1,
          }}
        >
          <button
            data-hover
            onClick={() => navigate("/login")}
            style={{
              background: "linear-gradient(135deg, #8b5cf6, #6366f1)",
              border: "none",
              color: "#fff",
              cursor: "pointer",
              fontSize: "16px",
              padding: "14px 28px",
              borderRadius: "8px",
              fontWeight: 500,
              display: "flex",
              alignItems: "center",
              gap: "8px",
              transition: "box-shadow 0.3s, transform 0.2s",
            }}
            onMouseEnter={(e) =>
              (e.currentTarget.style.boxShadow =
                "0 4px 24px rgba(139, 92, 246, 0.5)")
            }
            onMouseLeave={(e) =>
              (e.currentTarget.style.boxShadow = "none")
            }
          >
            Get Started
            <span>→</span>
          </button>
        </div>

        {/* Terminal Demo */}
        <div
          ref={terminalRef}
          style={{
            maxWidth: "700px",
            margin: "0 auto",
            background: "rgba(255,255,255,0.03)",
            border: "1px solid rgba(255,255,255,0.1)",
            borderRadius: "12px",
            overflow: "hidden",
            opacity: 0,
            transition: "box-shadow 0.3s",
            position: "relative",
            zIndex: 1,
          }}
        >
          <div
            style={{
              padding: "12px 16px",
              background: "rgba(255,255,255,0.03)",
              borderBottom: "1px solid rgba(255,255,255,0.1)",
              display: "flex",
              alignItems: "center",
              gap: "8px",
            }}
          >
            <div
              style={{
                width: "12px",
                height: "12px",
                borderRadius: "50%",
                background: "#ef4444",
              }}
            />
            <div
              style={{
                width: "12px",
                height: "12px",
                borderRadius: "50%",
                background: "#f59e0b",
              }}
            />
            <div
              style={{
                width: "12px",
                height: "12px",
                borderRadius: "50%",
                background: "#22c55e",
              }}
            />
            <span
              style={{
                marginLeft: "12px",
                fontSize: "13px",
                color: "#6b7280",
                fontFamily: "monospace",
              }}
            >
              deployment.log
            </span>
          </div>
          <div
            style={{
              padding: "20px",
              fontFamily: "monospace",
              fontSize: "14px",
              textAlign: "left",
              lineHeight: 1.8,
            }}
          >
            <div style={{ color: "#9ca3af" }}>→ Creating service...</div>
            <div style={{ color: "#9ca3af" }}>
              → Building Docker image:{" "}
              <span style={{ color: "#8b5cf6" }}>api:latest</span>
            </div>
            <div style={{ color: "#9ca3af" }}>→ Pushing to registry...</div>
            <div style={{ color: "#9ca3af" }}>→ Deploying container...</div>
            <div style={{ color: "#22c55e" }}>✓ Service deployed!</div>
          </div>
        </div>

        {/* Trust badges */}
        <div
          style={{
            display: "flex",
            justifyContent: "center",
            gap: "32px",
            marginTop: "24px",
          }}
        >
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: "8px",
              color: "#6b7280",
            }}
          >
            <span>🐳</span>
            <span style={{ fontSize: "14px" }}>Docker Powered</span>
          </div>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: "8px",
              color: "#6b7280",
            }}
          >
            <span>📊</span>
            <span style={{ fontSize: "14px" }}>Live Logs & Metrics</span>
          </div>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: "8px",
              color: "#6b7280",
            }}
          >
            <span>🎯</span>
            <span style={{ fontSize: "14px" }}>Visual Canvas Editor</span>
          </div>
        </div>
      </section>

      {/* Features Section */}
      <section id="features" style={{ padding: "60px 48px" }}>
        <div style={{ textAlign: "center", marginBottom: "64px" }}>
          <h2
            style={{ fontSize: "42px", fontWeight: 700, marginBottom: "16px" }}
          >
            Everything you need to{" "}
            <span
              style={{
                background: "linear-gradient(135deg, #8b5cf6, #3b82f6)",
                WebkitBackgroundClip: "text",
                WebkitTextFillColor: "transparent",
                backgroundClip: "text",
              }}
            >
              ship faster
            </span>
          </h2>
          <p
            style={{
              fontSize: "16px",
              color: "#9ca3af",
              maxWidth: "600px",
              margin: "0 auto",
            }}
          >
            A minimal control plane with an interactive 2D canvas. Deploy Docker
            containers, manage services, and monitor everything in real-time.
          </p>
        </div>

        <div
          ref={featuresRef}
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(4, 1fr)",
            gap: "24px",
            maxWidth: "1200px",
            margin: "0 auto",
          }}
        >
          {[
            {
              icon: "🎯",
              title: "2D Canvas Editor",
              desc: "Visualize your infrastructure on an interactive canvas. Drag, drop, and connect services with ease.",
              color: "#8b5cf6",
            },
            {
              icon: "🐳",
              title: "Docker Deployments",
              desc: "Deploy any Docker container with automatic builds. Support for Node.js, Next.js, React, and more.",
              color: "#3b82f6",
            },
            {
              icon: "📋",
              title: "Real-Time Logs",
              desc: "Stream logs via WebSocket as they happen. Debug issues instantly with our terminal-style log viewer.",
              color: "#22c55e",
            },
            {
              icon: "🔗",
              title: "Service Connections",
              desc: "Visually connect services to define dependencies and relationships. Auto-layout keeps it organized.",
              color: "#f97316",
            },
            {
              icon: "⚙️",
              title: "Environment Variables",
              desc: "Configure services with encrypted environment variables. Toggle visibility for security.",
              color: "#ec4899",
            },
            {
              icon: "🚀",
              title: "Auto Deploy",
              desc: "Services deploy automatically on creation. Track build status with live polling updates.",
              color: "#8b5cf6",
            },
            {
              icon: "🌐",
              title: "Nginx Routing",
              desc: "Automatic subdomain-based routing. Access services at http://service.localhost with zero config.",
              color: "#06b6d4",
            },
            {
              icon: "📦",
              title: "Multi-Service Projects",
              desc: "Organize services into projects. Manage databases, APIs, and frontends in one unified view.",
              color: "#f59e0b",
            },
          ].map((feature, i) => (
            <div
              key={i}
              data-animate
              style={{
                padding: "24px",
                background: "rgba(255,255,255,0.03)",
                border: "1px solid rgba(255,255,255,0.1)",
                borderRadius: "12px",
                opacity: 0,
                transition: "transform 0.3s, border-color 0.3s, box-shadow 0.3s",
                cursor: "default",
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.transform = "translateY(-8px)";
                e.currentTarget.style.borderColor = `${feature.color}60`;
                e.currentTarget.style.boxShadow = `0 20px 40px ${feature.color}15`;
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.transform = "translateY(0)";
                e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
                e.currentTarget.style.boxShadow = "none";
              }}
            >
              <div
                style={{
                  width: "48px",
                  height: "48px",
                  background: `${feature.color}20`,
                  borderRadius: "12px",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontSize: "24px",
                  marginBottom: "16px",
                  border: `1px solid ${feature.color}40`,
                  transition: "transform 0.3s",
                }}
                className="feature-icon"
              >
                {feature.icon}
              </div>
              <h3
                style={{
                  fontSize: "16px",
                  fontWeight: 600,
                  marginBottom: "8px",
                }}
              >
                {feature.title}
              </h3>
              <p style={{ fontSize: "14px", color: "#9ca3af", lineHeight: 1.6 }}>
                {feature.desc}
              </p>
            </div>
          ))}
        </div>
      </section>

      {/* How it Works Section */}
      <section id="how-it-works" style={{ padding: "60px 48px" }}>
        <div style={{ textAlign: "center", marginBottom: "64px" }}>
          <h2
            style={{ fontSize: "42px", fontWeight: 700, marginBottom: "16px" }}
          >
            Deploy in{" "}
            <span
              style={{
                background: "linear-gradient(135deg, #8b5cf6, #3b82f6)",
                WebkitBackgroundClip: "text",
                WebkitTextFillColor: "transparent",
                backgroundClip: "text",
              }}
            >
              four simple steps
            </span>
          </h2>
          <p
            style={{
              fontSize: "16px",
              color: "#9ca3af",
              maxWidth: "600px",
              margin: "0 auto",
            }}
          >
            From services to running containers in minutes. Visual editing meets
            powerful deployment automation.
          </p>
        </div>

        <div
          ref={stepsRef}
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(4, 1fr)",
            gap: "24px",
            maxWidth: "1200px",
            margin: "0 auto 64px",
          }}
        >
          {[
            {
              num: "01",
              icon: "📁",
              title: "Create Project",
              desc: "Start a new project to organize your services. Think of it as a workspace for your infrastructure.",
              color: "#8b5cf6",
            },
            {
              num: "02",
              icon: "🎯",
              title: "Design on Canvas",
              desc: "Add services to the 2D canvas. Drag and position them, then draw connections between dependencies.",
              color: "#06b6d4",
            },
            {
              num: "03",
              icon: "⚙️",
              title: "Configure & Deploy",
              desc: "Set environment variables, choose service types, and deploy. Automatic builds via Docker.",
              color: "#22c55e",
            },
            {
              num: "04",
              icon: "📋",
              title: "Monitor Live",
              desc: "Watch deployments in real-time. Stream logs, check status, and manage everything from one view.",
              color: "#f97316",
            },
          ].map((step, i) => (
            <div
              key={i}
              data-step
              style={{
                padding: "24px",
                background: "rgba(255,255,255,0.03)",
                border: "1px solid rgba(255,255,255,0.1)",
                borderRadius: "12px",
                position: "relative",
                opacity: 0,
                transition: "transform 0.3s, border-color 0.3s",
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.transform = "translateY(-4px)";
                e.currentTarget.style.borderColor = `${step.color}60`;
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.transform = "translateY(0)";
                e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
              }}
            >
              <div
                style={{
                  position: "absolute",
                  top: "-12px",
                  left: "24px",
                  padding: "4px 12px",
                  background: `${step.color}20`,
                  border: `1px solid ${step.color}40`,
                  borderRadius: "20px",
                  fontSize: "12px",
                  fontWeight: 600,
                  color: step.color,
                }}
              >
                {step.num}
              </div>
              <div
                style={{
                  width: "48px",
                  height: "48px",
                  background: `${step.color}20`,
                  borderRadius: "12px",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontSize: "24px",
                  marginBottom: "16px",
                  marginTop: "8px",
                  border: `1px solid ${step.color}40`,
                }}
              >
                {step.icon}
              </div>
              <h3
                style={{
                  fontSize: "16px",
                  fontWeight: 600,
                  marginBottom: "8px",
                }}
              >
                {step.title}
              </h3>
              <p style={{ fontSize: "14px", color: "#9ca3af", lineHeight: 1.6 }}>
                {step.desc}
              </p>
            </div>
          ))}
        </div>

      </section>

      {/* Pricing Section - Hidden
      <section id="pricing" style={{ padding: "80px 48px" }}>
        <div style={{ textAlign: "center", marginBottom: "64px" }}>
          <h2 style={{ fontSize: "42px", fontWeight: 700, marginBottom: "16px" }}>
            Simple,{" "}
            <span
              style={{
                background: "linear-gradient(135deg, #8b5cf6, #3b82f6)",
                WebkitBackgroundClip: "text",
                WebkitTextFillColor: "transparent",
                backgroundClip: "text",
              }}
            >
              transparent pricing
            </span>
          </h2>
          <p style={{ fontSize: "16px", color: "#9ca3af" }}>
            Start free and scale as you grow. No hidden fees, no surprises.
          </p>
        </div>

        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(3, 1fr)",
            gap: "24px",
            maxWidth: "1000px",
            margin: "0 auto",
          }}
        >
          {[
            {
              name: "Hobby",
              price: "$0",
              period: "/month",
              desc: "Perfect for side projects and learning",
              features: [
                "3 projects",
                "100 GB bandwidth",
                "Shared CPU",
                "512 MB RAM",
                "Community support",
                "Basic logs (24h retention)",
              ],
              cta: "Start Free",
              popular: false,
            },
            {
              name: "Pro",
              price: "$20",
              period: "/month",
              desc: "For professional developers and small teams",
              features: [
                "Unlimited projects",
                "1 TB bandwidth",
                "Dedicated CPU",
                "8 GB RAM",
                "Priority support",
                "Advanced logs (30 day retention)",
                "Custom domains",
                "Team collaboration",
              ],
              cta: "Start Pro Trial",
              popular: true,
            },
            {
              name: "Enterprise",
              price: "Custom",
              period: "",
              desc: "For organizations with advanced needs",
              features: [
                "Everything in Pro",
                "Unlimited bandwidth",
                "Custom resources",
                "SLA guarantee",
                "Dedicated support",
                "Unlimited log retention",
                "SSO / SAML",
                "Audit logs",
                "Private networking",
              ],
              cta: "Contact Sales",
              popular: false,
            },
          ].map((plan, i) => (
            <div
              key={i}
              style={{
                padding: "32px",
                background: plan.popular
                  ? "linear-gradient(135deg, rgba(139,92,246,0.2), rgba(59,130,246,0.1))"
                  : "rgba(255,255,255,0.03)",
                border: plan.popular
                  ? "1px solid rgba(139,92,246,0.5)"
                  : "1px solid rgba(255,255,255,0.1)",
                borderRadius: "16px",
                position: "relative",
              }}
            >
              {plan.popular && (
                <div
                  style={{
                    position: "absolute",
                    top: "-12px",
                    left: "50%",
                    transform: "translateX(-50%)",
                    padding: "6px 16px",
                    background: "linear-gradient(135deg, #8b5cf6, #6366f1)",
                    borderRadius: "20px",
                    fontSize: "12px",
                    fontWeight: 500,
                  }}
                >
                  ✨ Most Popular
                </div>
              )}
              <h3
                style={{
                  fontSize: "20px",
                  fontWeight: 600,
                  marginBottom: "8px",
                  textAlign: "center",
                }}
              >
                {plan.name}
              </h3>
              <div
                style={{
                  textAlign: "center",
                  marginBottom: "8px",
                }}
              >
                <span style={{ fontSize: "40px", fontWeight: 700 }}>{plan.price}</span>
                <span style={{ fontSize: "14px", color: "#6b7280" }}>{plan.period}</span>
              </div>
              <p
                style={{
                  fontSize: "14px",
                  color: "#9ca3af",
                  textAlign: "center",
                  marginBottom: "24px",
                  minHeight: "40px",
                }}
              >
                {plan.desc}
              </p>
              <div style={{ marginBottom: "24px" }}>
                {plan.features.map((feature, fi) => (
                  <div
                    key={fi}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: "8px",
                      marginBottom: "12px",
                      fontSize: "14px",
                    }}
                  >
                    <span style={{ color: "#8b5cf6" }}>✓</span>
                    <span>{feature}</span>
                  </div>
                ))}
              </div>
              <button
                onClick={() => navigate("/login")}
                style={{
                  width: "100%",
                  padding: "12px",
                  background: plan.popular
                    ? "linear-gradient(135deg, #8b5cf6, #6366f1)"
                    : "rgba(255,255,255,0.1)",
                  border: "none",
                  color: "#fff",
                  cursor: "pointer",
                  fontSize: "14px",
                  fontWeight: 500,
                  borderRadius: "8px",
                }}
              >
                {plan.cta}
              </button>
            </div>
          ))}
        </div>
      </section>
      */}

      {/* Footer */}
      <footer
        style={{
          padding: "40px 48px",
          borderTop: "1px solid rgba(255,255,255,0.1)",
          textAlign: "center",
        }}
      >
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            gap: "8px",
            marginBottom: "12px",
            cursor: "pointer",
          }}
          onClick={() => navigate("/")}
        >
          <img
            src={logoSvg}
            alt="Infra Pilot"
            style={{
              width: "24px",
              height: "24px",
            }}
          />
          <span style={{ fontSize: "16px", fontWeight: 600 }}>
            Infra Pilot
          </span>
        </div>
        <p
          style={{
            fontSize: "13px",
            color: "#6b7280",
            margin: "0 0 16px 0",
          }}
        >
          Visual infrastructure management for modern development teams
        </p>
        <p style={{ fontSize: "13px", color: "#9ca3af", margin: 0 }}>
          Made with ❤️ @2026 by MANWINDER SINGH
        </p>
      </footer>
    </div>
  );
}
