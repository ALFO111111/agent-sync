package com.alfo.agentsync;

import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.net.Uri;

import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

import java.util.regex.Matcher;
import java.util.regex.Pattern;

@CapacitorPlugin(name = "ExternalLinks")
public class ExternalLinksPlugin extends Plugin {
    private static final Pattern RELEASE = Pattern.compile(
        "^https://github\\.com/ALFO111111/agent-sync/releases/tag/v([0-9]+\\.[0-9]+\\.[0-9]+)$");
    private static final Pattern APK = Pattern.compile(
        "^https://github\\.com/ALFO111111/agent-sync/releases/download/v([0-9]+\\.[0-9]+\\.[0-9]+)/agent-sync-([0-9]+\\.[0-9]+\\.[0-9]+)-android\\.apk$");

    @PluginMethod
    public void open(PluginCall call) {
        String url = call.getString("url", "");
        Matcher apk = APK.matcher(url);
        if (!RELEASE.matcher(url).matches() && !(apk.matches() && apk.group(1).equals(apk.group(2)))) {
            call.reject("Only official release links are allowed");
            return;
        }
        try {
            Intent intent = new Intent(Intent.ACTION_VIEW, Uri.parse(url));
            intent.addCategory(Intent.CATEGORY_BROWSABLE);
            getActivity().startActivity(intent);
            call.resolve();
        } catch (ActivityNotFoundException e) {
            call.reject("No browser is available", e);
        } catch (SecurityException e) {
            call.reject("Could not open the release link", e);
        }
    }
}
