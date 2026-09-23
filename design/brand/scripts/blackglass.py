from pgeo3 import paths, C, RD, R, RC
VB = (300, 70, 600, 760)   # x, y, w, h around the mark (bbox 375..828 x 130..765)

def black_glass(px_w, uid="", bg=None, purple="#8C6CFF"):
    u = uid
    P = paths()
    h = round(px_w * VB[3] / VB[2])
    slabs = "".join(f'<path d="{P[k]}"></path>' for k in ("stem", "upper", "lower"))
    bgrect = f'<rect x="{VB[0]}" y="{VB[1]}" width="{VB[2]}" height="{VB[3]}" fill="{bg}"></rect>' if bg else ""
    return f'''<svg viewBox="{VB[0]} {VB[1]} {VB[2]} {VB[3]}" width="{px_w}" height="{h}" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
<defs>
<linearGradient id="base{u}" gradientUnits="userSpaceOnUse" x1="375" y1="130" x2="828" y2="765"><stop offset="0" stop-color="#17181E"></stop><stop offset="0.4" stop-color="#0A0A0E"></stop><stop offset="1" stop-color="#030304"></stop></linearGradient>
<linearGradient id="gloss{u}" gradientUnits="userSpaceOnUse" x1="375" y1="130" x2="600" y2="600"><stop offset="0" stop-color="#FFFFFF" stop-opacity="0.16"></stop><stop offset="1" stop-color="#FFFFFF" stop-opacity="0.02"></stop></linearGradient>
<clipPath id="clip{u}">{slabs}</clipPath>
<filter id="bev{u}" x="-5%" y="-5%" width="110%" height="110%" color-interpolation-filters="sRGB">
<feGaussianBlur in="SourceAlpha" stdDeviation="3.2" result="blur"></feGaussianBlur>
<feSpecularLighting in="blur" surfaceScale="4" specularConstant="1.4" specularExponent="70" lighting-color="#FFFFFF" result="spec"><feDistantLight azimuth="225" elevation="38"></feDistantLight></feSpecularLighting>
<feComposite in="spec" in2="SourceAlpha" operator="in" result="specIn"></feComposite>
<feSpecularLighting in="blur" surfaceScale="4" specularConstant="1.0" specularExponent="40" lighting-color="{purple}" result="rim"><feDistantLight azimuth="45" elevation="30"></feDistantLight></feSpecularLighting>
<feComposite in="rim" in2="SourceAlpha" operator="in" result="rimIn"></feComposite>
<feComposite in="SourceGraphic" in2="specIn" operator="arithmetic" k1="0" k2="1" k3="0.75" k4="0" result="lit"></feComposite>
<feComposite in="lit" in2="rimIn" operator="arithmetic" k1="0" k2="1" k3="0.35" k4="0"></feComposite>
</filter>
<filter id="shadow{u}" x="-20%" y="-20%" width="140%" height="140%"><feGaussianBlur in="SourceAlpha" stdDeviation="10"></feGaussianBlur><feOffset dy="12" result="o"></feOffset><feFlood flood-color="#0B0A1F" flood-opacity="0.35"></feFlood><feComposite in2="o" operator="in"></feComposite></filter>
<radialGradient id="tubeU{u}" gradientUnits="userSpaceOnUse" cx="{C[0]}" cy="{C[1]}" r="{R}">
<stop offset="{RC / R:.3f}" stop-color="#000" stop-opacity="0.35"></stop>
<stop offset="{(RC + R) / 2 / R - 0.004:.3f}" stop-color="#000" stop-opacity="0.05"></stop>
<stop offset="{(RC + R) / 2 / R:.3f}" stop-color="#FFF" stop-opacity="0.13"></stop>
<stop offset="{(RC + R) / 2 / R + 0.12:.3f}" stop-color="#FFF" stop-opacity="0.07"></stop>
<stop offset="1" stop-color="#FFF" stop-opacity="0.02"></stop></radialGradient>
<radialGradient id="tubeL{u}" gradientUnits="userSpaceOnUse" cx="{C[0]}" cy="{C[1]}" r="{R}">
<stop offset="{RC / R:.3f}" stop-color="#FFF" stop-opacity="0.05"></stop>
<stop offset="{(RC + R) / 2 / R - 0.004:.3f}" stop-color="#FFF" stop-opacity="0.11"></stop>
<stop offset="{(RC + R) / 2 / R:.3f}" stop-color="#000" stop-opacity="0.12"></stop>
<stop offset="1" stop-color="#000" stop-opacity="0.45"></stop></radialGradient>
<linearGradient id="tubeS{u}" gradientUnits="userSpaceOnUse" x1="375" y1="0" x2="485" y2="0">
<stop offset="0" stop-color="#FFF" stop-opacity="0.06"></stop>
<stop offset="0.35" stop-color="#FFF" stop-opacity="0.02"></stop>
<stop offset="1" stop-color="#000" stop-opacity="0.30"></stop></linearGradient>
<clipPath id="cu{u}"><path d="{P['upper']}"></path></clipPath>
<clipPath id="cl{u}"><path d="{P['lower']}"></path></clipPath>
<clipPath id="cs{u}"><path d="{P['stem']}"></path></clipPath>
<radialGradient id="sph{u}" cx="0.36" cy="0.30" r="0.78"><stop offset="0" stop-color="#6A6F8E"></stop><stop offset="0.28" stop-color="#2A2C40"></stop><stop offset="0.75" stop-color="#0C0C14"></stop><stop offset="1" stop-color="#050508"></stop></radialGradient>
<radialGradient id="sphr{u}" cx="0.72" cy="0.78" r="0.5"><stop offset="0" stop-color="{purple}" stop-opacity="0.55"></stop><stop offset="1" stop-color="{purple}" stop-opacity="0"></stop></radialGradient>
<radialGradient id="sphh{u}" cx="0.5" cy="0.5" r="0.5"><stop offset="0" stop-color="#FFFFFF" stop-opacity="0.30"></stop><stop offset="1" stop-color="#FFFFFF" stop-opacity="0"></stop></radialGradient>
</defs>
{bgrect}
<g filter="url(#shadow{u})">{slabs}<circle cx="{C[0]}" cy="{C[1]}" r="{RD}"></circle></g>
<g filter="url(#bev{u})" fill="url(#base{u})">{slabs}</g>
<path d="{P['upper']}" fill="url(#tubeU{u})"></path>
<path d="{P['lower']}" fill="url(#tubeL{u})"></path>
<path d="{P['stem']}" fill="url(#tubeS{u})"></path>
<g clip-path="url(#clip{u})">
<path d="M300 70 H600 L412 520 L300 590 Z" fill="url(#gloss{u})"></path>
<rect x="300" y="130" width="600" height="6" fill="#FFFFFF" fill-opacity="0.10"></rect>
</g>
<g fill="none" stroke-width="1.1">{"".join(f'<path d="{P[k]}" stroke="#8A8DA0" stroke-opacity="0.45"></path>' for k in ("stem", "upper", "lower"))}</g>
<circle cx="{C[0]}" cy="{C[1]}" r="{RD}" fill="url(#sph{u})"></circle>
<circle cx="{C[0]}" cy="{C[1]}" r="{RD}" fill="url(#sphr{u})"></circle>
<ellipse cx="{C[0] - 26}" cy="{C[1] - 30}" rx="24" ry="14" transform="rotate(-35 {C[0] - 26} {C[1] - 30})" fill="url(#sphh{u})"></ellipse>
<circle cx="{C[0]}" cy="{C[1]}" r="{RD - 0.8}" fill="none" stroke="#FFFFFF" stroke-opacity="0.10" stroke-width="1.6"></circle>
</svg>'''
