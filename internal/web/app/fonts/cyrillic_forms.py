# SPDX-License-Identifier: Apache-2.0
"""Give Sofia Sans's Cyrillic files the forms framy's languages use.

Sofia Sans draws Cyrillic in its Bulgarian forms by default (a д like a g,
a т like an m, a в like a ϐ) and keeps the forms Russian, Ukrainian and
Belarusian use as local forms for Russian only (locl, RUS). A browser asks
for them only on a page whose language is Russian, so the site in Ukrainian
or Belarusian, and a Cyrillic prompt on a page in any other language, came
out in the Bulgarian forms. framy speaks no Bulgarian, so this makes the
Russian forms the ones the cmap points at and drops the glyphs nothing
reaches any more. internal/anim does the same for the cards by shaping
Ukrainian and Belarusian as Russian (ShapingLang).

Run it from the repository's root after replacing a Cyrillic file; running it
again changes nothing:

    python3 internal/web/app/fonts/cyrillic_forms.py

It needs fontTools and brotli (pip install fonttools brotli).
"""

import glob
import os
import sys

from fontTools import subset
from fontTools.ttLib import TTFont

HERE = os.path.dirname(os.path.abspath(__file__))


def local_forms(font):
    """The glyph of each default glyph in the locl lookups of cyrl RUS."""
    gsub = font["GSUB"].table
    features = gsub.FeatureList.FeatureRecord
    out = {}
    for script in gsub.ScriptList.ScriptRecord:
        if script.ScriptTag != "cyrl":
            continue
        for lang in script.Script.LangSysRecord:
            if lang.LangSysTag.strip() != "RUS":
                continue
            for i in lang.LangSys.FeatureIndex:
                if features[i].FeatureTag != "locl":
                    continue
                for li in features[i].Feature.LookupListIndex:
                    for st in gsub.LookupList.Lookup[li].SubTable:
                        st = getattr(st, "ExtSubTable", st)
                        out.update(getattr(st, "mapping", {}))
    return out


def fix(path):
    font = TTFont(path)
    forms = local_forms(font)
    changed = 0
    for table in font["cmap"].tables:
        for cp, glyph in list(table.cmap.items()):
            if glyph in forms:
                table.cmap[cp] = forms[glyph]
                changed += 1
    if not changed:
        return False
    # Keep everything the cmap reaches, with every feature and name, and let
    # the glyphs only the Bulgarian forms used go.
    opts = subset.Options()
    opts.layout_features = ["*"]
    opts.layout_scripts = ["*"]
    opts.name_IDs = ["*"]
    opts.name_languages = ["*"]
    opts.glyph_names = True
    opts.notdef_outline = True
    opts.hinting = True
    opts.legacy_kern = True
    opts.flavor = "woff2"
    sub = subset.Subsetter(opts)
    sub.populate(unicodes=font.getBestCmap().keys())
    sub.subset(font)
    font.flavor = "woff2"
    font.save(path)
    return True


def main():
    files = sorted(glob.glob(os.path.join(HERE, "*cyrillic*.woff2")))
    if not files:
        sys.exit("no Cyrillic files beside the script")
    for path in files:
        print(os.path.basename(path), "fixed" if fix(path) else "already has them")


if __name__ == "__main__":
    main()
