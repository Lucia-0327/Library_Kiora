// React 入口：StrictMode 仅在开发环境帮助发现副作用问题，不改变生产业务逻辑。
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>
);
