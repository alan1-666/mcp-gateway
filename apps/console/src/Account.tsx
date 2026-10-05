import { useEffect, useState, type FormEvent } from "react";
import { APIClient, messageOf } from "./api";
import type { Identity } from "./types";

export type CloudSession = {
  api: APIClient;
  identity: Identity;
  username: string;
  cloud: true;
};
export async function restoreSession(
  api = new APIClient(),
): Promise<CloudSession> {
  const s = await api.request<{
    identity: Identity;
    username: string;
    csrf_token: string;
  }>("/auth/session");
  api.setCSRF(s.csrf_token);
  return { api, identity: s.identity, username: s.username, cloud: true };
}

export function CloudLogin({
  onConnect,
}: {
  onConnect: (s: CloudSession) => void;
}) {
  const [invite] = useState(
    () =>
      new URLSearchParams(window.location.hash.slice(1)).get("invite") ?? "",
  );
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // Invitation secrets stay out of request URLs, browser history and referrers.
  useEffect(() => {
    if (invite) window.history.replaceState(null, "", window.location.pathname);
  }, [invite]);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const api = new APIClient();
      await api.request(invite ? "/auth/accept" : "/auth/login", {
        method: "POST",
        body: { username, password, ...(invite ? { token: invite } : {}) },
      });
      setPassword("");
      onConnect(await restoreSession(api));
    } catch (e) {
      setError(messageOf(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="connect-layout">
      <section className="connect-story">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            M
          </span>
          <div>
            MCP Gateway<span>CONTROL WORKSPACE</span>
          </div>
        </div>
        <div className="connect-story-body">
          <span className="eyebrow">YOUR TEAM’S TOOL ACCESS LAYER</span>
          <h1>
            Shared tools.
            <br />
            Accountable actions.
          </h1>
          <p>
            Manage integrations, review sensitive actions, and follow every
            operation from request to outcome.
          </p>
          <div className="flow-strip">
            <span>Discover</span>
            <i />
            <span>Authorize</span>
            <i />
            <span>Execute</span>
          </div>
        </div>
        <div className="connect-story-foot">
          <span className="connection-dot" /> A private workspace for your team.
        </div>
      </section>
      <section className="connect-form-wrap">
        <form className="connect-form" onSubmit={submit}>
          <span className="eyebrow">INVITATION-ONLY ACCESS</span>
          <h2>{invite ? "Join your workspace" : "Welcome back"}</h2>
          <p>
            {invite
              ? "Choose your account details to accept this invitation."
              : "Sign in with your workspace account. New here? Ask an administrator for an invitation."}
          </p>
          {error && (
            <div role="alert" className="notice notice-error">
              {error}
            </div>
          )}
          <label htmlFor="username">Username</label>
          <input
            id="username"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            minLength={3}
            maxLength={64}
            pattern="[a-zA-Z0-9][a-zA-Z0-9._\-]{2,63}"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            required
            disabled={busy}
          />
          <label htmlFor="password">Password</label>
          <input
            id="password"
            type="password"
            autoComplete={invite ? "new-password" : "current-password"}
            minLength={invite ? 12 : 1}
            maxLength={72}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            disabled={busy}
          />
          <p className="field-help">
            {invite
              ? "At least 12 characters. This invitation can only be used once."
              : "Forgot your password? Contact the workspace administrator."}
          </p>
          <button className="button primary connect-button" disabled={busy}>
            {busy ? "Please wait…" : invite ? "Create account" : "Sign in"}
          </button>
          <div className="connect-note">
            <p>
              Your workspace permissions determine which tools and actions you
              can access.
            </p>
          </div>
        </form>
        <span className="connect-caption">
          MCP Gateway · Enterprise tool governance
        </span>
      </section>
    </main>
  );
}

type Member = { id: string; username: string; role: string; disabled: boolean };
type Invite = {
  id: string;
  role: string;
  expires_at: string;
  consumed_at: string | null;
  revoked_at: string | null;
};
type Key = {
  id: string;
  name: string;
  expires_at: string;
  revoked_at: string | null;
};
type Event = {
  id: string;
  action: string;
  actor_id: string;
  subject_id: string;
  created_at: string;
};
const roles = ["viewer", "operator", "approver", "admin"];
export function Account({
  api,
  identity,
  onSignedOut,
  refreshVersion,
}: {
  api: APIClient;
  identity: Identity;
  onSignedOut: () => void;
  refreshVersion: string;
}) {
  const [members, setMembers] = useState<Member[]>([]);
  const [invites, setInvites] = useState<Invite[]>([]);
  const [keys, setKeys] = useState<Key[]>([]);
  const [events, setEvents] = useState<Event[]>([]);
  const [role, setRole] = useState("operator");
  const [keyName, setKeyName] = useState("");
  const [secret, setSecret] = useState<{ label: string; value: string } | null>(
    null,
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [current, setCurrent] = useState("");
  const [password, setPassword] = useState("");
  const isAdmin = identity.role === "admin";
  async function load() {
    const [k, m, i, e] = await Promise.all([
      api.request<{ items: Key[] }>("/auth/keys"),
      isAdmin
        ? api.request<{ items: Member[] }>("/auth/members")
        : Promise.resolve({ items: [] }),
      isAdmin
        ? api.request<{ items: Invite[] }>("/auth/invites")
        : Promise.resolve({ items: [] }),
      isAdmin
        ? api.request<{ items: Event[] }>("/auth/events")
        : Promise.resolve({ items: [] }),
    ]);
    setKeys(k.items);
    setMembers(m.items);
    setInvites(i.items);
    setEvents(e.items);
    setError("");
  }
  useEffect(() => {
    void load().catch((e) => setError(messageOf(e)));
  }, [api, identity.id, refreshVersion]);
  async function action(fn: () => Promise<unknown>, reload = true) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await fn();
      if (reload) await load();
    } catch (e) {
      setError(messageOf(e));
    } finally {
      setBusy(false);
    }
  }
  async function createInvite(e: FormEvent) {
    e.preventDefault();
    await action(async () => {
      const x = await api.request<{ url: string }>("/auth/invites", {
        method: "POST",
        body: { role },
      });
      setSecret({
        label: "Invitation link · expires in 48 hours",
        value: x.url,
      });
    });
  }
  async function createKey(e: FormEvent) {
    e.preventDefault();
    await action(async () => {
      const x = await api.request<{ token: string }>("/auth/keys", {
        method: "POST",
        body: { name: keyName },
      });
      setSecret({
        label: "API key · shown once · expires in 30 days",
        value: x.token,
      });
      setKeyName("");
    });
  }
  return (
    <div className="account-panels">
      {error && (
        <div role="alert" className="notice notice-error">
          {error}
        </div>
      )}
      {secret && (
        <section className="panel account-panel" role="status">
          <h2>{secret.label}</h2>
          <p>
            Copy this value now. It will not be shown again after you leave this
            page.
          </p>
          <textarea
            aria-label={secret.label}
            readOnly
            value={secret.value}
            rows={3}
            onFocus={(e) => e.currentTarget.select()}
          />
          <button className="button secondary" onClick={() => setSecret(null)}>
            Dismiss
          </button>
        </section>
      )}
      {isAdmin && (
        <>
          <section className="panel account-panel">
            <h2>Team members</h2>
            <p>
              Changes revoke the member’s sessions and API keys. Your own access
              must be changed by another administrator.
            </p>
            {members.map((m) => (
              <div className="account-row" key={m.id}>
                <div>
                  <strong>{m.username}</strong>
                  <small>{m.disabled ? "Disabled" : "Active"}</small>
                </div>
                <select
                  aria-label={`Role for ${m.username}`}
                  value={m.role}
                  disabled={busy || m.id === identity.id}
                  onChange={(e) =>
                    void action(() =>
                      api.request(`/auth/members/${m.id}`, {
                        method: "POST",
                        body: { role: e.target.value, disabled: m.disabled },
                      }),
                    )
                  }
                >
                  {roles.map((r) => (
                    <option key={r}>{r}</option>
                  ))}
                </select>
                <button
                  className="button secondary"
                  disabled={busy || m.id === identity.id}
                  onClick={() =>
                    void action(() =>
                      api.request(`/auth/members/${m.id}`, {
                        method: "POST",
                        body: { role: m.role, disabled: !m.disabled },
                      }),
                    )
                  }
                >
                  {m.disabled ? "Enable" : "Disable"}
                </button>
              </div>
            ))}
          </section>
          <section className="panel account-panel">
            <h2>Invite a teammate</h2>
            <p>
              Send the link to the intended person. Anyone holding it can claim
              the selected role once.
            </p>
            <form className="account-row" onSubmit={createInvite}>
              <label htmlFor="invite-role">Role</label>
              <select
                id="invite-role"
                value={role}
                onChange={(e) => setRole(e.target.value)}
              >
                {roles.map((r) => (
                  <option key={r}>{r}</option>
                ))}
              </select>
              <button className="button primary" disabled={busy}>
                Create invitation
              </button>
            </form>
            {invites.map((i) => (
              <div className="account-row" key={i.id}>
                <div>
                  <strong>{i.role} invitation</strong>
                  <small>
                    {i.consumed_at
                      ? "Accepted"
                      : i.revoked_at
                        ? "Revoked"
                        : `Expires ${new Date(i.expires_at).toLocaleString()}`}
                  </small>
                </div>
                {!i.consumed_at && !i.revoked_at && (
                  <button
                    className="button secondary"
                    disabled={busy}
                    onClick={() =>
                      void action(() =>
                        api.request(`/auth/invites/${i.id}/revoke`, {
                          method: "POST",
                          body: {},
                        }),
                      )
                    }
                  >
                    Revoke
                  </button>
                )}
              </div>
            ))}
          </section>
        </>
      )}
      <section className="panel account-panel">
        <h2>Personal API keys</h2>
        <p>
          Use these keys with MCP clients and automation. Each key has your
          workspace role; account administration requires signing in here.
        </p>
        <form className="account-row" onSubmit={createKey}>
          <label htmlFor="key-name">Name</label>
          <input
            id="key-name"
            value={keyName}
            onChange={(e) => setKeyName(e.target.value)}
            placeholder="e.g. Development client"
            required
            maxLength={80}
          />
          <button className="button primary" disabled={busy}>
            Create key
          </button>
        </form>
        {keys.map((k) => (
          <div className="account-row" key={k.id}>
            <div>
              <strong>{k.name}</strong>
              <small>
                {k.revoked_at
                  ? "Revoked"
                  : `Expires ${new Date(k.expires_at).toLocaleString()}`}
              </small>
            </div>
            {!k.revoked_at && (
              <button
                className="button secondary"
                disabled={busy}
                onClick={() =>
                  void action(() =>
                    api.request(`/auth/keys/${k.id}/revoke`, {
                      method: "POST",
                      body: {},
                    }),
                  )
                }
              >
                Revoke
              </button>
            )}
          </div>
        ))}
      </section>
      <section className="panel account-panel">
        <h2>Change password</h2>
        <p>This signs out all sessions and revokes all your API keys.</p>
        <form
          className="account-password"
          onSubmit={(e) => {
            e.preventDefault();
            void action(async () => {
              await api.request("/auth/password", {
                method: "POST",
                body: { current, password },
              });
              setCurrent("");
              setPassword("");
              onSignedOut();
            }, false);
          }}
        >
          <label htmlFor="current-password">Current password</label>
          <input
            id="current-password"
            type="password"
            autoComplete="current-password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            required
            maxLength={72}
          />
          <label htmlFor="new-password">New password</label>
          <input
            id="new-password"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            minLength={12}
            maxLength={72}
          />
          <button className="button secondary" disabled={busy}>
            Update password
          </button>
        </form>
      </section>
      {isAdmin && (
        <section className="panel account-panel">
          <h2>Account activity</h2>
          <p>Latest 100 account and access changes.</p>
          {events.map((e) => (
            <div className="account-row" key={e.id}>
              <div>
                <strong>{e.action.replaceAll("_", " ").toLowerCase()}</strong>
                <small>
                  {members.find((m) => m.id === e.actor_id)?.username ??
                    e.actor_id}{" "}
                  · {new Date(e.created_at).toLocaleString()}
                </small>
              </div>
            </div>
          ))}
        </section>
      )}
    </div>
  );
}
