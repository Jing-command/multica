import { createLogger } from "@/shared/logger";
import { ApiClient } from "./client";

export { ApiClient } from "./client";
export type { LoginResponse } from "./client";
export { WSClient } from "./ws-client";

const API_BASE_URL = process.env.NEXT_PUBLIC_API_URL || "";

export const api = new ApiClient(API_BASE_URL, { logger: createLogger("api") });

// Initialize persisted workspace selection on load.
if (typeof window !== "undefined") {
  const wsId = localStorage.getItem("multica_workspace_id");
  if (wsId) {
    api.setWorkspaceId(wsId);
  }
}
