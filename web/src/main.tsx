import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { DevSimulator } from "./DevSimulator";
import "./styles.css";

const isDevRoute =
  window.location.pathname === "/dev" ||
  window.location.pathname.startsWith("/dev/");

createRoot(document.getElementById("root")!).render(
  <StrictMode>{isDevRoute ? <DevSimulator /> : <App />}</StrictMode>,
);
