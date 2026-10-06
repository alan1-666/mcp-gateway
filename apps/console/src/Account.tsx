import { useI18n } from "./i18n";
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
  const { t } = useI18n();
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
        <a className="brand" href="/" aria-label={t("Rillgate home")}>
          <span className="brand-mark" aria-hidden="true">
            <svg viewBox="0 0 32 32" fill="none">
              <path
                d="M8 26V6h8a7 7 0 0 1 0 14H8m9 0 8 6"
                stroke="currentColor"
                strokeWidth="2.5"
              />
            </svg>
          </span>
          <div>
            Rillgate<span>{t("Team workspace")}</span>
          </div>
        </a>
        <div className="connect-story-body">
          <span className="eyebrow">{t("YOUR TEAM’S TOOL ACCESS LAYER")}</span>
          <h1>
            {t("Shared tools.")}
            <br />
            {t("Accountable actions.")}
          </h1>
          <p>
            {t(
              "Manage integrations, review sensitive actions, and follow every operation from request to outcome.",
            )}
          </p>
          <div className="flow-strip">
            <span>{t("Discover")}</span>
            <i />
            <span>{t("Authorize")}</span>
            <i />
            <span>{t("Execute")}</span>
          </div>
        </div>
        <div className="connect-story-foot">
          <span className="connection-dot" />{" "}
          {t("A private workspace for your team.")}
        </div>
      </section>
      <section className="connect-form-wrap">
        <form className="connect-form" onSubmit={submit}>
          <span className="eyebrow">{t("INVITATION-ONLY ACCESS")}</span>
          <h2>{invite ? t("Join your workspace") : t("Welcome back")}</h2>
          <p>
            {invite
              ? t("Choose your account details to accept this invitation.")
              : t(
                  "Sign in with your workspace account. New here? Ask an administrator for an invitation.",
                )}
          </p>
          {error && (
            <div role="alert" className="notice notice-error">
              {t(error)}
            </div>
          )}
          <label htmlFor="username">{t("Username")}</label>
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
          <label htmlFor="password">{t("Password")}</label>
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
              ? t(
                  "At least 12 characters. This invitation can only be used once.",
                )
              : t("Forgot your password? Contact the workspace administrator.")}
          </p>
          <button className="button primary connect-button" disabled={busy}>
            {busy
              ? t("Please wait…")
              : invite
                ? t("Create account")
                : t("Sign in")}
          </button>
          <div className="connect-note">
            <p>
              {t(
                "Your workspace permissions determine which tools and actions you can access.",
              )}
            </p>
          </div>
        </form>
        <span className="connect-caption">
          {t("Rillgate · Enterprise tool governance")}
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
  const { t, locale } = useI18n();
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
          {t(error)}
        </div>
      )}
      {secret && (
        <section className="panel account-panel" role="status">
          <h2>{t(secret.label)}</h2>
          <p>
            {t(
              "Copy this value now. It will not be shown again after you leave this page.",
            )}
          </p>
          <textarea
            aria-label={t(secret.label)}
            readOnly
            value={secret.value}
            rows={3}
            onFocus={(e) => e.currentTarget.select()}
          />
          <button className="button secondary" onClick={() => setSecret(null)}>
            {t("Dismiss")}
          </button>
        </section>
      )}
      {isAdmin && (
        <>
          <section className="panel account-panel">
            <h2>{t("Team members")}</h2>
            <p>
              {t(
                "Changes revoke the member’s sessions and API keys. Your own access must be changed by another administrator.",
              )}
            </p>
            {members.map((m) => (
              <div className="account-row" key={m.id}>
                <div>
                  <strong>{m.username}</strong>
                  <small>{m.disabled ? t("Disabled") : t("Active")}</small>
                </div>
                <select
                  aria-label={t("Role for {name}", { name: m.username })}
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
                    <option key={r} value={r}>
                      {t(r)}
                    </option>
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
                  {m.disabled ? t("Enable") : t("Disable")}
                </button>
              </div>
            ))}
          </section>
          <section className="panel account-panel">
            <h2>{t("Invite a teammate")}</h2>
            <p>
              {t(
                "Send the link to the intended person. Anyone holding it can claim the selected role once.",
              )}
            </p>
            <form className="account-row" onSubmit={createInvite}>
              <label htmlFor="invite-role">{t("Role")}</label>
              <select
                id="invite-role"
                value={role}
                onChange={(e) => setRole(e.target.value)}
              >
                {roles.map((r) => (
                  <option key={r} value={r}>
                    {t(r)}
                  </option>
                ))}
              </select>
              <button className="button primary" disabled={busy}>
                {t("Create invitation")}
              </button>
            </form>
            {invites.map((i) => (
              <div className="account-row" key={i.id}>
                <div>
                  <strong>{t("{role} invitation", { role: t(i.role) })}</strong>
                  <small>
                    {i.consumed_at
                      ? t("Accepted")
                      : i.revoked_at
                        ? t("Revoked")
                        : t("Expires {date}", {
                            date: new Date(i.expires_at).toLocaleString(locale),
                          })}
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
                    {t("Revoke")}
                  </button>
                )}
              </div>
            ))}
          </section>
        </>
      )}
      <section className="panel account-panel">
        <h2>{t("Personal API keys")}</h2>
        <p>
          {t(
            "Use these keys with MCP clients and automation. Each key has your workspace role; account administration requires signing in here.",
          )}
        </p>
        <form className="account-row" onSubmit={createKey}>
          <label htmlFor="key-name">{t("Name")}</label>
          <input
            id="key-name"
            value={keyName}
            onChange={(e) => setKeyName(e.target.value)}
            placeholder={t("e.g. Development client")}
            required
            maxLength={80}
          />
          <button className="button primary" disabled={busy}>
            {t("Create key")}
          </button>
        </form>
        {keys.map((k) => (
          <div className="account-row" key={k.id}>
            <div>
              <strong>{k.name}</strong>
              <small>
                {k.revoked_at
                  ? t("Revoked")
                  : t("Expires {date}", {
                      date: new Date(k.expires_at).toLocaleString(locale),
                    })}
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
                {t("Revoke")}
              </button>
            )}
          </div>
        ))}
      </section>
      <section className="panel account-panel">
        <h2>{t("Change password")}</h2>
        <p>{t("This signs out all sessions and revokes all your API keys.")}</p>
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
          <label htmlFor="current-password">{t("Current password")}</label>
          <input
            id="current-password"
            type="password"
            autoComplete="current-password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            required
            maxLength={72}
          />
          <label htmlFor="new-password">{t("New password")}</label>
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
            {t("Update password")}
          </button>
        </form>
      </section>
      {isAdmin && (
        <section className="panel account-panel">
          <h2>{t("Account activity")}</h2>
          <p>{t("Latest 100 account and access changes.")}</p>
          {events.map((e) => (
            <div className="account-row" key={e.id}>
              <div>
                <strong>{e.action.replaceAll("_", " ").toLowerCase()}</strong>
                <small>
                  {members.find((m) => m.id === e.actor_id)?.username ??
                    e.actor_id}{" "}
                  · {new Date(e.created_at).toLocaleString(locale)}
                </small>
              </div>
            </div>
          ))}
        </section>
      )}
    </div>
  );
}
