package net.poweur.app;

import android.os.Bundle;

import com.getcapacitor.BridgeActivity;

public class MainActivity extends BridgeActivity {
    @Override
    public void onCreate(Bundle savedInstanceState) {
        // App-local plugins are not discovered the way iOS discovers them from
        // the ObjC runtime; the bridge has to be told, before it is built.
        registerPlugin(PoweurKeystorePlugin.class);
        super.onCreate(savedInstanceState);
    }
}
