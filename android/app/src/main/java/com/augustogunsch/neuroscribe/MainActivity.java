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
import android.content.Intent;
import android.content.SharedPreferences;
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

import androidx.activity.OnBackPressedCallback;
import androidx.appcompat.app.AppCompatActivity;

import java.io.InputStream;
import java.security.SecureRandom;
import java.util.HashMap;
import java.util.Map;

public class MainActivity extends AppCompatActivity {

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
                return asset("web" + path);
            }

            for (String p : SERVER_PATHS) {
                if (path.equals(p) || path.startsWith(p)) {
                    return null;
                }
            }

            // Every other address inside the app is one document — /notes/…,
            // /settings, /trash all render from the local store.
            return asset("web/index.html");
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

    /* asset returns a bundled file, or null so the request goes to the network. */
    private WebResourceResponse asset(String name) {
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
        return new WebResourceResponse(mime != null ? mime : "application/octet-stream", "utf-8", in);
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
