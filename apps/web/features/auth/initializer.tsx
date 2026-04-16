"use client";

import { useEffect, type ReactNode } from "react";
import { useAuthStore } from "./store";
import { useWorkspaceStore } from "@/features/workspace";
import { api } from "@/shared/api";
import { createLogger } from "@/shared/logger";
import { hasLoggedInCookie, setLoggedInCookie, clearLoggedInCookie } from "./auth-cookie";

const logger = createLogger("auth");

/**
 * Initializes browser auth state from the session cookie on mount.
 * Fires getMe() and listWorkspaces() in parallel when a logged-in cookie exists.
 */
export function AuthInitializer({ children }: { children: ReactNode }) {
  useEffect(() => {
    if (!hasLoggedInCookie()) {
      api.setToken(null);
      api.setWorkspaceId(null);
      clearLoggedInCookie();
      useAuthStore.setState({ isLoading: false });
      return;
    }

    const wsId = localStorage.getItem("multica_workspace_id");

    // Fire getMe and listWorkspaces in parallel using the cookie-backed session.
    const mePromise = api.getMe();
    const wsPromise = api.listWorkspaces();

    Promise.all([mePromise, wsPromise])
      .then(([user, wsList]) => {
        setLoggedInCookie();
        useAuthStore.setState({ user, isLoading: false });
        useWorkspaceStore.getState().hydrateWorkspace(wsList, wsId);
      })
      .catch((err) => {
        logger.error("auth init failed", err);
        api.setToken(null);
        api.setWorkspaceId(null);
        localStorage.removeItem("multica_token");
        localStorage.removeItem("multica_workspace_id");
        clearLoggedInCookie();
        useAuthStore.setState({ user: null, isLoading: false });
      });
  }, []);

  return <>{children}</>;
}
