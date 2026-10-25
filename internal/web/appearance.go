package web

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"strings"

	"github.com/BlancoKat/wikiwright/internal/color"
	"github.com/BlancoKat/wikiwright/internal/store"
)

func appearanceAdmin(sess store.Session) bool {
	return sess.User.ID != 0 && !sess.NeedsTOTP && sess.User.IsAdmin && strings.TrimSpace(sess.Grants) == ""
}

func (s *Site) accentHex() string {
	if s.Store != nil {
		v, err := s.Store.Setting("accent")
		if err != nil {
			log.Printf("wiki %s: accent setting: %v", s.Cfg.ID, err)
		} else if hex, ok := color.Normalize(v); ok {
			return hex
		}
	}
	if hex, ok := color.Normalize(s.Cfg.Accent); ok {
		return hex
	}
	return color.DefaultAccent
}

func (s *Site) defaultAccent() string {
	if hex, ok := color.Normalize(s.Cfg.Accent); ok {
		return hex
	}
	return color.DefaultAccent
}

func (s *Site) theme() color.Theme {
	return color.Build(s.accentHex())
}

func (s *Site) accentCSS() string {
	return `<style id="wikiwright-accent">:root{` + s.theme().RootCSS() + `}</style>`
}

func (s *Site) handleAppearance(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionView(r)
	admin := appearanceAdmin(sess)
	notice := ""
	if r.Method == http.MethodPost {
		if !admin {
			s.fail(w, r, http.StatusForbidden, "Only an administrator can change the appearance.")
			return
		}
		if err := r.ParseForm(); err != nil {
			s.fail(w, r, http.StatusBadRequest, "Could not read the form.")
			return
		}
		if !tokensEqual(sess.CSRF, r.FormValue("csrf")) {
			s.fail(w, r, http.StatusForbidden, "The form token did not match. Reload and try again.")
			return
		}
		switch r.FormValue("wpAction") {
		case "reset":
			if err := s.Store.DeleteSetting("accent"); err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not reset the color.")
				return
			}
			log.Printf("wiki %s appearance: %s reset the accent color", s.Cfg.ID, sess.User.Name)
			http.Redirect(w, r, s.pageURL("Special:Appearance")+"&reset=1", http.StatusSeeOther)
			return
		case "save", "":
			hex, ok := accentFromPost(r)
			if !ok {
				notice = `<div class="wikiwright-error">Choose a swatch, or pick a color with the wheel. Colors are stored as #rrggbb.</div>`
			} else if err := s.Store.SetSetting("accent", hex); err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not save the color.")
				return
			} else {
				log.Printf("wiki %s appearance: %s set the accent color to %s", s.Cfg.ID, sess.User.Name, hex)
				http.Redirect(w, r, s.pageURL("Special:Appearance")+"&saved=1", http.StatusSeeOther)
				return
			}
		default:
			notice = `<div class="wikiwright-error">That action is not recognized.</div>`
		}
	} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case r.URL.Query().Get("saved") == "1":
		notice = `<div class="wikiwright-notice">Saved the accent color for this wiki.</div>` + notice
	case r.URL.Query().Get("reset") == "1":
		notice = `<div class="wikiwright-notice">Reset the accent color to the default.</div>` + notice
	}
	nonce := ""
	if admin {
		nonce = scriptNonce()
		w.Header().Set("Content-Security-Policy", strings.Replace(csp, "script-src 'none'", "script-src 'nonce-"+nonce+"'", 1))
	}
	s.writeSpecial(w, r, "Appearance", s.appearanceBody(admin, sess, notice, nonce))
}

func accentFromPost(r *http.Request) (string, bool) {
	choice := strings.TrimSpace(r.FormValue("choice"))
	raw := strings.TrimSpace(r.FormValue("custom"))
	if choice != "" && choice != "custom" {
		if !color.IsPreset(choice) {
			return "", false
		}
		raw = choice
	}
	return color.Normalize(raw)
}

func (s *Site) appearanceBody(admin bool, sess store.Session, notice, nonce string) string {
	th := s.theme()
	fallback := s.defaultAccent()
	source := "the built-in pine"
	if strings.TrimSpace(s.Cfg.Accent) != "" {
		source = "the configuration"
	}
	var b strings.Builder
	b.WriteString(notice)
	b.WriteString(`<form id="wikiwright-appearance" class="wikiwright-appearance wikiwright-login" method="post" action="` + esc(s.pageURL("Special:Appearance")) + `">`)
	if admin {
		b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
	}
	if admin {
		b.WriteString(`<fieldset>`)
	} else {
		b.WriteString(`<fieldset disabled>`)
	}
	b.WriteString(`<p>The accent color is the header and the sidebar. Text on that color is chosen for contrast. Links and borders use a darker or lighter shade of the same color when the accent itself would be hard to see on the page.</p>`)
	b.WriteString(`<p>Current color <code>` + esc(th.Accent) + `</code>.</p>`)
	b.WriteString(`<div class="wikiwright-swatches" role="radiogroup" aria-label="Accent colors">`)
	custom := !color.IsPreset(th.Accent)
	for _, p := range color.Presets() {
		checked := ""
		if !custom && p.Hex == th.Accent {
			checked = ` checked`
		}
		b.WriteString(`<label class="wikiwright-swatch"><input type="radio" name="choice" value="` + esc(p.Hex) + `"` + checked + `><span style="background:` + p.Hex + `"></span>` + esc(p.Name) + `</label>`)
	}
	checked := ""
	if custom {
		checked = ` checked`
	}
	b.WriteString(`<label class="wikiwright-swatch"><input type="radio" name="choice" value="custom"` + checked + `><span class="wikiwright-swatch-custom"></span>Custom</label>`)
	b.WriteString(`</div>`)
	b.WriteString(`<label>Color wheel <input type="color" name="custom" id="wikiwright-accent-picker" value="` + esc(th.Accent) + `"></label>`)
	b.WriteString(`</fieldset>`)
	b.WriteString(`<div class="wikiwright-accent-preview" id="wikiwright-accent-preview"><strong>` + esc(s.Cfg.Name) + `</strong><span>Header and sidebar</span><a href="#wikiwright-accent-preview">A link on the page</a></div>`)
	b.WriteString(`<dl class="wikiwright-contrast">`)
	b.WriteString(`<div><dt>Header text</dt><dd id="wikiwright-ink-ratio">` + esc(color.FormatRatio(th.InkRatio)) + `</dd></div>`)
	b.WriteString(`<div><dt>Links on the page</dt><dd id="wikiwright-link-ratio">` + esc(color.FormatRatio(th.LinkRatio)) + `</dd></div>`)
	b.WriteString(`<div><dt>Borders on the page</dt><dd id="wikiwright-border-ratio">` + esc(color.FormatRatio(th.BorderRatio)) + `</dd></div>`)
	b.WriteString(`</dl>`)
	if warn := th.Warning(); warn != "" {
		b.WriteString(`<div class="wikiwright-error" id="wikiwright-accent-warn">` + esc(warn) + `</div>`)
	} else if admin {
		// Empty hook for the live preview. No failure text until a ratio is below AA.
		b.WriteString(`<div id="wikiwright-accent-warn" hidden></div>`)
	}
	if admin {
		b.WriteString(`<p class="wikiwright-muted">Reset removes the saved color and uses ` + esc(source) + ` (` + esc(fallback) + `).</p>`)
		b.WriteString(`<div class="wikiwright-form-actions">`)
		b.WriteString(`<button type="submit" name="wpAction" value="save">Save color</button>`)
		b.WriteString(`<button type="submit" name="wpAction" value="reset">Reset to default</button>`)
		b.WriteString(`</div>`)
		b.WriteString(`<script nonce="` + esc(nonce) + `">` + appearanceScript + `</script>`)
	} else {
		b.WriteString(`<p>Only an administrator can save a color. This page is read-only.</p>`)
	}
	b.WriteString(`</form>`)
	return b.String()
}

func scriptNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "wikiwright"
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// appearanceScript mirrors internal/color so the preview can update before a save.
// The server recomputes the same theme when the form is posted.
const appearanceScript = `(function(){
var form=document.getElementById("wikiwright-appearance");
if(!form)return;
var picker=document.getElementById("wikiwright-accent-picker");
var inkEl=document.getElementById("wikiwright-ink-ratio");
var linkEl=document.getElementById("wikiwright-link-ratio");
var borderEl=document.getElementById("wikiwright-border-ratio");
var warn=document.getElementById("wikiwright-accent-warn");
var root=document.documentElement;
function hexToRgb(hex){
  if(!hex||hex.charAt(0)!=="#"||hex.length!==7)return null;
  var n=parseInt(hex.slice(1),16);
  if(!isFinite(n))return null;
  return {r:((n>>16)&255)/255,g:((n>>8)&255)/255,b:(n&255)/255};
}
function lin(c){return c<=0.04045?c/12.92:Math.pow((c+0.055)/1.055,2.4);}
function lum(c){return 0.2126*lin(c.r)+0.7152*lin(c.g)+0.0722*lin(c.b);}
function contrast(a,b){var l1=lum(a),l2=lum(b);if(l1<l2){var t=l1;l1=l2;l2=t;}return (l1+0.05)/(l2+0.05);}
function mix(a,b,t){return {r:a.r*(1-t)+b.r*t,g:a.g*(1-t)+b.g*t,b:a.b*(1-t)+b.b*t};}
function hexOf(c){
  function h(v){v=Math.round(Math.max(0,Math.min(1,v))*255);var s=v.toString(16);return s.length===1?"0"+s:s;}
  return "#"+h(c.r)+h(c.g)+h(c.b);
}
function minC(c,bgs){var m=21;for(var i=0;i<bgs.length;i++){var r=contrast(c,bgs[i]);if(r<m)m=r;}return m;}
function fit(color,bgs,min){
  var ratio=minC(color,bgs);
  if(ratio>=min-1e-6)return {color:color,ratio:ratio,ok:true};
  var black={r:0,g:0,b:0},white={r:1,g:1,b:1};
  var found=false,best,bestT=2,bestRatio=0;
  var targets=[black,white];
  for(var ti=0;ti<targets.length;ti++){
    for(var i=1;i<=50;i++){
      var t=i/50,c=mix(color,targets[ti],t),r=minC(c,bgs);
      if(r+1e-9>=min){
        if(!found||t<bestT-1e-9||(Math.abs(t-bestT)<1e-9&&r>bestRatio)){found=true;best=c;bestT=t;bestRatio=r;}
        break;
      }
    }
  }
  if(!found){var fb=black,fr=minC(black,bgs),wr=minC(white,bgs);if(wr>fr){fb=white;fr=wr;}return {color:fb,ratio:fr,ok:false};}
  return {color:best,ratio:bestRatio,ok:true};
}
var paper=hexToRgb("#f3eee4"),card=hexToRgb("#fffdf8"),near=hexToRgb("#141210");
var white={r:1,g:1,b:1},black={r:0,g:0,b:0};
function apply(hex){
  var bg=hexToRgb(hex);if(!bg)return;
  var cw=contrast(bg,white),cn=contrast(bg,near);
  var ink=white,inkRatio=cw;
  if(cn>cw){ink=near;inkRatio=cn;}
  var inkOK=inkRatio>=4.5-1e-6;
  if(!inkOK){var cb=contrast(bg,black);if(cb>inkRatio){ink=black;inkRatio=cb;}inkOK=inkRatio>=4.5-1e-6;}
  var border=fit(bg,[paper,card],3),link=fit(bg,[paper,card],4.5);
  var plateTarget=ink.r<0.5?black:white,plate=bg;
  for(var i=8;i>=0;i--){var p=mix(bg,plateTarget,i/40);if(contrast(p,ink)>=4.5-1e-6){plate=p;break;}}
  var label=ink;
  for(var j=20;j>=0;j--){var lab=mix(ink,bg,j/20*0.45);if(contrast(lab,bg)>=4.5-1e-6){label=lab;break;}}
  root.style.setProperty("--pine",hexOf(bg));
  root.style.setProperty("--pine-2",hexOf(plate));
  root.style.setProperty("--foam",hexOf(ink));
  root.style.setProperty("--accent",hexOf(bg));
  root.style.setProperty("--accent-ink",hexOf(ink));
  root.style.setProperty("--accent-border",hexOf(border.color));
  root.style.setProperty("--teal",hexOf(link.color));
  root.style.setProperty("--sidebar-label",hexOf(label));
  function fmt(n){return n.toFixed(2)+":1";}
  inkEl.textContent=fmt(inkRatio);
  linkEl.textContent=fmt(link.ratio);
  borderEl.textContent=fmt(border.ratio);
  var parts=[];
  if(inkRatio<4.5)parts.push("Header text is "+fmt(inkRatio)+" and needs 4.5:1.");
  if(border.ratio<3)parts.push("Borders are "+fmt(border.ratio)+" and need 3:1 against the page.");
  if(link.ratio<4.5)parts.push("Links are "+fmt(link.ratio)+" and need 4.5:1 against the page.");
  if(!parts.length){warn.hidden=true;warn.textContent="";warn.className="";}
  else{warn.hidden=false;warn.className="wikiwright-error";warn.textContent="This color cannot meet WCAG AA. "+parts.join(" ")+" The closest readable shades are still applied.";}
}
function selected(){
  var radios=form.querySelectorAll('input[name="choice"]'),choice="custom";
  for(var i=0;i<radios.length;i++){if(radios[i].checked)choice=radios[i].value;}
  return choice==="custom"?picker.value:choice;
}
form.addEventListener("change",function(ev){
  if(ev.target&&ev.target.name==="choice"&&ev.target.value!=="custom")picker.value=ev.target.value;
  if(ev.target===picker){var custom=form.querySelector('input[name="choice"][value="custom"]');if(custom)custom.checked=true;}
  apply(selected());
});
picker.addEventListener("input",function(){
  var custom=form.querySelector('input[name="choice"][value="custom"]');
  if(custom)custom.checked=true;
  apply(picker.value);
});
})();`
