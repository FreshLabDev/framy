// A GIF result (GPT Image 2.6) plays as a picture does, over and over, and
// stops on its first frame: a canvas with that frame lies over the animation
// until it plays again. With reduced motion it opens stopped and waits for
// Play, as a video does. The thumbnail stands in while the GIF loads.
import { h, ic } from "./dom.js";
import { motion } from "./motion.js";
import { t } from "./i18n.js";

export const isGif = (res) => res?.content_type === "image/gif";

// The GIF in a box that fills its frame, as an <img> of a picture does.
export function gifView(res, alt) {
  const img = h("img", { src: res.url, alt, decoding: "async" });
  const box = h("span", { class: "gifv" }, img);
  box._img = img;
  box._held = false;
  if (!motion()) hold(box, true);
  return box;
}

// Stops the GIF on its first frame, or lets it play. A canvas draws an
// animated picture's first frame (HTML, drawImage), whatever it shows now.
function hold(box, on) {
  box._held = on;
  const img = box._img;
  if (!on) {
    box._still?.remove();
    img.style.visibility = "";
    box.dispatchEvent(new Event("play"));
    return;
  }
  const draw = () => {
    if (!box._held || !img.naturalWidth) return;
    const c = box._still || (box._still = h("canvas", { "aria-hidden": "true" }));
    c.width = img.naturalWidth;
    c.height = img.naturalHeight;
    c.getContext("2d").drawImage(img, 0, 0);
    box.append(c);
    // Out of sight it stops costing frames.
    img.style.visibility = "hidden";
  };
  if (img.complete && img.naturalWidth) draw();
  else {
    // Until it loads, nothing moves: the GIF stays out of sight.
    img.style.visibility = "hidden";
    img.addEventListener("load", draw, { once: true });
  }
  box.dispatchEvent(new Event("pause"));
}

// The button that stops the GIF and plays it again, as a video's in a frame.
export function gifButton(box, cls = "tpp") {
  const b = h("button", { type: "button", class: cls, onclick: (e) => {
    e.stopPropagation();
    hold(box, !box._held);
  } });
  const sync = () => {
    const on = !box._held, label = t(on ? "player.pause" : "player.play");
    b.replaceChildren(ic(on ? "pause" : "play"));
    b.setAttribute("aria-label", label);
    b.title = label;
    b.classList.toggle("still", !on);
  };
  box.addEventListener("play", sync);
  box.addEventListener("pause", sync);
  sync();
  return b;
}
