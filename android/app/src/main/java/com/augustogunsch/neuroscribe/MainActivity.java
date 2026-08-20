package com.augustogunsch.neuroscribe;

/*
 * The app is the website, with the frontend brought along.
 *
 * Not a Trusted Web Activity, and not for want of trying: a TWA is a shortcut
 * to the live site, so deploying the server would change the app underneath
 * whoever installed it. The whole point of shipping an APK here is that it
 * changes when someone installs a new one and at no other time.
 *
 * So the scripts, styles, fonts and shell live in assets/web/ inside the APK,
 * and this answers requests for them — under the real https origin, which is
 * the load-bearing detail. The page believes it is at the site because as far
 * as the WebView is concerned it is: an intercepted response is attributed to
 * the URL that was asked for. The session cookie, the CSRF cookie, the
 * Content-Security-Policy, crypto.subtle (which needs a secure context) and
 * every same-origin fetch to /sync therefore work exactly as they do in a
 * browser, with no special case anywhere in the web code.
 *
 * Anything not in the bundle falls through to the network. That split is the
 * design in one line: the app's own frontend comes from the APK and is frozen;
 * the account — /sync, /login, /account, and the optional runtimes — comes from
 * the server and is live.
 */

import android.annotation.SuppressLint;
import android.content.ContentResolver;
import android.content.ContentValues;
import android.content.Intent;
import android.content.SharedPreferences;
import android.provider.MediaStore;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.util.Base64;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Toast;

import androidx.activity.ComponentActivity;
import androidx.activity.OnBackPressedCallback;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.security.SecureRandom;
import java.util.HashMap;
import java.util.Map;

public class MainActivity extends ComponentActivity {

    private WebView web;

    /* Guessed from the extension rather than from the file, because the bundle
     * holds seven kinds of thing and getting one of them wrong is not subtle:
     * a script served as text/plain is a script the browser refuses to run. */
    private static final Map<String, String> MIME = new HashMap<>();
    static {
        MIME.put("js", "text/javascript");
        MIME.put("css", "text/css");
        MIME.put("html", "text/html");
        MIME.put("json", "application/json");
        MIME.put("svg", "image/svg+xml");
        MIME.put("woff2", "font/woff2");
        MIME.put("wasm", "application/wasm");
        MIME.put("webmanifest", "application/manifest+json");
    }

    /* The three policies from handlers.go and runner.go, restated here.
     *
     * Worth a word, because it is the one piece of the server's security
     * posture the app has to repeat rather than receive: these responses never
     * come from the server, so nothing would attach a policy to them. Serving
     * them bare would make the app the only place the frontend runs with no
     * CSP at all.
     *
     * They are three and not one for the same reason they are three there. The
     * page may not eval; the snippet runner must, because running a JavaScript
     * snippet is eval by definition, which is why it is framed with an opaque
     * origin and holds no session, no keys and no DOM of ours; and the
     * typesetter's worker must, because wasm-bindgen's start-up builds a
     * function from a string. Handing the page the runner's policy would undo
     * the point of having two.
     *
     * The frontend and this file are frozen in the same build, so they only
     * ever have to agree at build time — but if the policy over there gains a
     * directive, this needs it too. */
    private static final String CSP =
            "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "
            + "font-src 'self'; img-src 'self' data: blob:; connect-src 'self'; "
            + "worker-src 'self' blob:; frame-src 'self'; manifest-src 'self'; "
            + "form-action 'self'; base-uri 'none'";

    /* 'self' would match nothing from an opaque origin, so the runner's policy
     * names this server. */
    private static final String RUNNER_CSP =
            "default-src 'none'; script-src " + BuildConfig.ORIGIN
            + " 'unsafe-eval' 'wasm-unsafe-eval'; connect-src " + BuildConfig.ORIGIN
            + "; worker-src blob:; child-src blob:; base-uri 'none'";

    private static final String WORKER_CSP =
            "default-src 'self'; script-src 'self' 'unsafe-eval' 'wasm-unsafe-eval'; "
            + "connect-src 'self'; worker-src 'self'; base-uri 'none'";

    private static String cspFor(String path) {
        if (path.equals("/static/runner.html")) {
            return RUNNER_CSP;
        }
        if (path.equals("/static/typst-worker.js")) {
            return WORKER_CSP;
        }
        return CSP;
    }

    /* Paths the server owns. Everything else inside the app is the shell. */
    private static final String[] SERVER_PATHS = {
        "/sync", "/login", "/logout", "/register", "/verify", "/auth/",
        "/account", "/healthz", "/altcha/", "/pyodide/", "/typst/", "/download/",
    };

    @SuppressLint("SetJavaScriptEnabled")
    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);

        web = new WebView(this);
        setContentView(web);

        web.getSettings().setJavaScriptEnabled(true);
        web.getSettings().setDomStorageEnabled(true);
        // IndexedDB is where the notes are; without it the app is a viewer for
        // an empty account.
        web.getSettings().setDatabaseEnabled(true);
        // The frontend is ours and the server is ours; nothing else loads.
        web.getSettings().setAllowFileAccess(false);
        web.getSettings().setAllowContentAccess(false);
        web.getSettings().setJavaScriptCanOpenWindowsAutomatically(false);
        web.getSettings().setSupportMultipleWindows(false);

        CookieManager.getInstance().setAcceptCookie(true);
        CookieManager.getInstance().setAcceptThirdPartyCookies(web, false);

        web.addJavascriptInterface(new Native(), "NeuroscribeNative");
        web.setWebViewClient(new Client());

        if (state != null) {
            web.restoreState(state);
        } else {
            web.loadUrl(BuildConfig.ORIGIN + "/");
        }

        // Back should walk the app's own history before it leaves the app,
        // which is what a person means by it here.
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                if (web.canGoBack()) {
                    web.goBack();
                } else {
                    setEnabled(false);
                    getOnBackPressedDispatcher().onBackPressed();
                }
            }
        });
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        web.saveState(out);
    }

    private class Client extends WebViewClient {
        @Override
        public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
            Uri url = request.getUrl();
            // Only our own origin is ever served from the APK.
            if (!BuildConfig.ORIGIN_HOST.equals(url.getHost())) {
                return null;
            }
            String path = url.getPath();
            if (path == null || path.contains("..")) {
                return null;
            }

            // Assets, when the bundle has them. When it does not, fall through
            // to the server rather than answering 404: the sign-in page is
            // still rendered server-side and may come to ask for a file this
            // build has never heard of, and a login that broke over it would be
            // a poor way to find out the app is behind.
            if (path.startsWith("/static/") || path.startsWith("/strings/")
                    || path.equals("/manifest.webmanifest")) {
                return asset("web" + path, path);
            }

            for (String p : SERVER_PATHS) {
                if (path.equals(p) || path.startsWith(p)) {
                    return null;
                }
            }

            // Every other address inside the app is one document — /notes/…,
            // /settings, /trash all render from the local store.
            return asset("web/index.html", "/");
        }

        @Override
        public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
            Uri url = request.getUrl();
            if (BuildConfig.ORIGIN_HOST.equals(url.getHost())) {
                return false; // ours: the WebView handles it
            }
            // Anything else — the repository link, the KaTeX reference — is the
            // web, and the web belongs in a browser.
            try {
                startActivity(new Intent(Intent.ACTION_VIEW, url));
            } catch (Exception ignored) {
                return false;
            }
            return true;
        }
    }

    /* asset returns a bundled file, or null so the request goes to the network.
     * urlPath is the address it was asked for by, which decides its policy. */
    private WebResourceResponse asset(String name, String urlPath) {
        // /strings/en.json is asked for with the suffix and stored with it, but
        // be forgiving: the frontend has used both spellings.
        InputStream in = open(name);
        if (in == null && !name.endsWith(".json") && name.startsWith("web/strings/")) {
            in = open(name + ".json");
        }
        if (in == null) {
            return null;
        }
        String ext = "";
        int dot = name.lastIndexOf('.');
        if (dot >= 0) {
            ext = name.substring(dot + 1).toLowerCase();
        }
        String mime = MIME.get(ext);
        // The server sends these on every response, and a bundled one has to
        // carry them too or the app would be the one place the app runs
        // without them. The policy is the server's, minus frame-ancestors,
        // which has no meaning inside a WebView that frames nothing.
        Map<String, String> headers = new HashMap<>();
        headers.put("Content-Security-Policy", cspFor(urlPath));
        headers.put("X-Content-Type-Options", "nosniff");
        headers.put("Referrer-Policy", "no-referrer");
        return new WebResourceResponse(
                mime != null ? mime : "application/octet-stream", "utf-8",
                200, "OK", headers, in);
    }

    /* A name Downloads will accept, and one that cannot point anywhere else:
     * the page chooses it, and a page is not something to take a path from. */
    private static String safeName(String name) {
        String clean = name == null ? "" : name.replaceAll("[/\\\\:*?\"<>|]+", "_").trim();
        if (clean.isEmpty()) {
            clean = "neuroscribe-export";
        }
        return clean.length() > 120 ? clean.substring(0, 120) : clean;
    }

    private void toast(String message) {
        runOnUiThread(() -> Toast.makeText(MainActivity.this, message, Toast.LENGTH_LONG).show());
    }

    private InputStream open(String name) {
        try {
            return getAssets().open(name);
        } catch (Exception e) {
            return null;
        }
    }

    /*
     * The one thing a browser cannot offer: an identity for this installation
     * that survives the app's data being cleared.
     *
     * It lives in SharedPreferences, which Android keeps in the app's private
     * storage, so clearing site data inside the WebView — the thing that loses
     * the browser's own id — does not touch it. Uninstalling does, and that is
     * the right boundary: a fresh install is a fresh device, and it will adopt
     * the account's PIN on its first sign-in. See static/lock.js.
     */
    public class Native {
        @JavascriptInterface
        public String deviceId() {
            SharedPreferences prefs = getSharedPreferences("neuroscribe", MODE_PRIVATE);
            String id = prefs.getString("device_id", "");
            if (id.isEmpty()) {
                byte[] raw = new byte[9];
                new SecureRandom().nextBytes(raw);
                id = Base64.encodeToString(raw,
                        Base64.URL_SAFE | Base64.NO_PADDING | Base64.NO_WRAP);
                prefs.edit().putString("device_id", id).apply();
            }
            return id;
        }

        /*
         * Saving an exported file.
         *
         * A WebView ignores blob: downloads outright — no download, no error,
         * nothing — so a PDF export would look like it had worked and leave
         * nothing behind. The page hands the bytes over here instead, in
         * chunks, because a whole document in one call across this bridge is
         * asking to hit a limit nobody documents.
         *
         * The chunks land in the app's own cache directory and only become a
         * file in Downloads once the last one arrives, so an export that fails
         * halfway leaves no half a PDF for someone to open and puzzle over.
         */
        @JavascriptInterface
        public void saveFile(String name, String mime, String base64,
                             boolean first, boolean last) {
            try {
                File staging = new File(getCacheDir(), "export.part");
                if (first && staging.exists() && !staging.delete()) {
                    throw new IOException("could not clear the last export");
                }
                byte[] bytes = Base64.decode(base64, Base64.DEFAULT);
                try (FileOutputStream out = new FileOutputStream(staging, !first)) {
                    out.write(bytes);
                }
                if (!last) {
                    return;
                }
                ContentValues meta = new ContentValues();
                meta.put(MediaStore.Downloads.DISPLAY_NAME, safeName(name));
                meta.put(MediaStore.Downloads.MIME_TYPE, mime);
                meta.put(MediaStore.Downloads.IS_PENDING, 1);
                ContentResolver resolver = getContentResolver();
                Uri target = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, meta);
                if (target == null) {
                    throw new IOException("no room in Downloads");
                }
                try (InputStream in = new FileInputStream(staging);
                     OutputStream out = resolver.openOutputStream(target)) {
                    byte[] buf = new byte[64 * 1024];
                    int n;
                    while ((n = in.read(buf)) > 0) {
                        out.write(buf, 0, n);
                    }
                }
                meta.clear();
                meta.put(MediaStore.Downloads.IS_PENDING, 0);
                resolver.update(target, meta, null, null);
                staging.delete();
                toast(getString(R.string.saved_to_downloads, safeName(name)));
            } catch (Exception e) {
                toast(getString(R.string.export_failed));
            }
        }

        @JavascriptInterface
        public String appVersion() {
            return BuildConfig.VERSION_NAME;
        }

        @JavascriptInterface
        public String platform() {
            return "android-" + Build.VERSION.SDK_INT;
        }
    }
}
