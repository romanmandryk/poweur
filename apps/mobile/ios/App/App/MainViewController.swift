import UIKit
import Capacitor

/**
 * The bridge controller, subclassed for one reason: registering the app's own
 * plugin.
 *
 * Capacitor discovers plugins that ship as Swift packages. `PoweurKeystore`
 * lives in the app target instead — it is not a reusable library, it is this
 * app's custody — so nothing scans for it, and the bridge has to be handed the
 * instance as soon as it exists.
 */
class MainViewController: CAPBridgeViewController {
    override open func capacitorDidLoad() {
        bridge?.registerPluginInstance(PoweurKeystorePlugin())
    }
}
