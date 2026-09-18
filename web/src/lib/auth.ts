/**
 * Authenticated user context.
 *
 * A context rather than prop drilling: the profile menu is six levels below the
 * router and is the only consumer, so threading it through every layout component
 * would be noise.
 */

import { createContext, useContext } from "react";
import type { AuthUser } from "./api";

export interface AuthValue {
  user: AuthUser;
  signOut: () => void;
}

const AuthContext = createContext<AuthValue | null>(null);

export const AuthProvider = AuthContext.Provider;

/** Returns the signed-in user. Only valid inside the authenticated shell. */
export function useAuth(): AuthValue {
  const v = useContext(AuthContext);
  if (!v) {
    // A clear failure beats a silent undefined: reaching here means a component
    // rendered outside the auth gate, which is a wiring bug.
    throw new Error("useAuth called outside the authenticated shell");
  }
  return v;
}
