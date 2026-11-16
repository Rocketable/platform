import { createRoot } from "react-dom/client";
import { App } from "./ui";

if (document.getElementById("posthog-config")) void import("./posthog");

createRoot(document.getElementById("root")!).render(<App />);
