export const VAULT_PAGE_SIZE = 30;

const messages = {
  local_storage: "无法读取此实例的 GPT 凭据，请从 KeyPouch 重新打开实例。",
  local_changed: "此实例的 GPT 凭据已变更，请重新点击「登录 GPT」。",
  invalid_site: "请输入 KeyPouch 站点的 HTTPS 根地址，本机回环地址也可使用 HTTP。",
  invalid_query: "账号查询参数无效，请重新搜索。",
  network: "无法连接 KeyPouch，请检查站点地址、网络和扩展的站点访问权限。",
  timeout: "读取 KeyPouch 超时，请检查网络后重试。",
  unauthorized: "KeyPouch 会话已过期，请在本实例登录 KeyPouch 后刷新。",
  password_change_required: "请先在 KeyPouch 站点修改密码，再返回刷新。",
  forbidden: "当前 KeyPouch 会话没有访问权限，请在站点检查登录状态。",
  not_found: "KeyPouch 未找到请求的内容，请刷新后重试。",
  service_error: "KeyPouch 暂时无法处理请求，请稍后再试。",
  invalid_response: "KeyPouch 返回的数据格式有误，请检查站点地址后重试。",
  record_changed: "所选账号已变更或删除，请刷新后重新选择。",
  unsupported: "当前 KeyPouch 版本尚未提供插件授权，请先更新服务端。",
  key_storage: "无法读取此实例的插件密钥，请检查 Chromium 是否允许本地存储。",
  grant_required: "此实例的账号授权已失效，请返回插件检查授权。",
  totp_unavailable: "这条记录当前无法生成验证码，请在登录页手动完成验证。",
};

export class VaultError extends Error {
  constructor(code) {
    super(messages[code]);
    this.name = "VaultError";
    this.code = code;
  }
}

export function normalizeVaultSite(raw) {
  const site = typeof raw === "string" ? raw.trim() : "";
  // Validate the original authority before URL can normalize away userinfo, a
  // path (e.g. /..), an empty query/fragment, backslashes or control characters.
  const authority = /^https?:\/\/([^\s/\\@?#]+)\/*$/i.exec(site);
  if (!authority || authority[1].endsWith(":") || /[\u0000-\u001f\u007f]/.test(site)) {
    throw new VaultError("invalid_site");
  }
  let url;
  try {
    url = new URL(site);
  } catch {
    throw new VaultError("invalid_site");
  }
  const host = url.hostname;
  const loopback = host === "localhost" || host === "[::1]" || /^127\.\d+\.\d+\.\d+$/.test(host) ||
    /^\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}\]$/.test(host);
  if (host.includes("*") || url.port === "0" || (url.protocol === "http:" && !loopback)) throw new VaultError("invalid_site");
  return url.origin;
}

// Called only from the extension page. Chromium sends the site's session
// cookie with host permission; the extension never needs its value.
async function readVault(site, path, signal) {
  const url = normalizeVaultSite(site) + path;
  const controller = new AbortController();
  const cancel = () => controller.abort(new DOMException("读取 KeyPouch 已取消。", "AbortError"));
  if (signal?.aborted) cancel();
  else signal?.addEventListener("abort", cancel, { once: true });
  const timeout = setTimeout(() => controller.abort(new VaultError("timeout")), 15_000);
  let failureCode = "network";
  try {
    controller.signal.throwIfAborted();
    const response = await fetch(url, {
      method: "GET",
      credentials: "include",
      cache: "no-store",
      redirect: "error",
      referrerPolicy: "no-referrer",
      headers: { Accept: "application/json" },
      signal: controller.signal,
    });
    if (response.redirected || (response.url && response.url !== url)) throw new VaultError("network");
    if (response.status === 401) throw new VaultError("unauthorized");
    if (response.status === 404) throw new VaultError("not_found");
    if (response.status === 403) {
      const body = await response.json().catch(() => null);
      throw new VaultError(body?.error === "password_change_required" ? "password_change_required" : "forbidden");
    }
    if (response.status !== 200) throw new VaultError("service_error");
    failureCode = "invalid_response";
    return await response.json();
  } catch (error) {
    controller.signal.throwIfAborted();
    if (error instanceof VaultError) throw error;
    throw new VaultError(failureCode);
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener("abort", cancel);
  }
}

function positiveID(id) {
  return Number.isSafeInteger(id) && id > 0;
}

export async function getVaultSession(site, signal) {
  let session;
  try {
    session = await readVault(site, "/api/session", signal);
  } catch (err) {
    if (err.code === "unauthorized") return false;
    throw err;
  }
  if (!positiveID(session?.user?.id)) throw new VaultError("invalid_response");
  if (session.user.must_change_password) throw new VaultError("password_change_required");
  return !session.user.disabled;
}

export function accountEmail(value) {
  const email = typeof value === "string" ? value.trim() : "";
  const parts = email.split("@");
  if (email.length > 254 || parts.length !== 2) return "";
  const [local, domain] = parts;
  const domainLabels = domain.split(".");
  if (!local || local.length > 64 || domainLabels.length < 2) return "";
  // Dot-separated atoms allow ordinary plus aliases while excluding display
  // names, addresses embedded in URLs and free-form record usernames.
  if (!local.split(".").every(atom => /^[\p{L}\p{M}\p{N}!#$%&'*+/=?^_`{|}~-]+$/u.test(atom))) return "";
  if (!domainLabels.every(label => /^[\p{L}\p{N}](?:[\p{L}\p{M}\p{N}-]{0,61}[\p{L}\p{M}\p{N}])?$/u.test(label))) return "";
  return email;
}

export async function listVaultAccounts(site, query = "", offset = 0, signal) {
  if (typeof query !== "string" || !Number.isSafeInteger(offset) || offset < 0) throw new VaultError("invalid_query");
  const params = new URLSearchParams({
    q: query.trim(), offset: String(offset), limit: String(VAULT_PAGE_SIZE), sort: "username", order: "asc",
  });
  const page = await readVault(site, "/api/records?" + params, signal);
  if (!Array.isArray(page?.items) || !Number.isSafeInteger(page.total) || page.total < 0) {
    throw new VaultError("invalid_response");
  }
  const items = [];
  for (const record of page.items) {
    const email = accountEmail(record?.username);
    if (email && positiveID(record.id) && record.deleted_at === null) {
      items.push({ id: record.id, email, hasPassword: record.has_password === true, hasTOTP: record.has_totp === true });
    }
  }
  // Offsets count raw KeyPouch records, including non-email usernames.
  return { items, total: page.total };
}

export async function getVaultAccount(site, id, expectedEmail, signal) {
  const selected = accountEmail(expectedEmail);
  if (!positiveID(id) || !selected) throw new VaultError("record_changed");
  const record = await readVault(site, "/api/records/" + id, signal);
  const email = accountEmail(record?.username);
  if (record?.id !== id || record.deleted_at !== null || email !== selected) throw new VaultError("record_changed");
  if (typeof record.password !== "string" || typeof record.totp_secret !== "string") throw new VaultError("invalid_response");
  return { email, password: record.password, totpSecret: record.totp_secret };
}
