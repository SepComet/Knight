"""Build the UI's non-ASCII Fusion Pixel font; requires fontTools."""
from pathlib import Path
from fontTools import subset
from fontTools.ttLib import TTFont


def main():
    root = Path(__file__).resolve().parent.parent
    directory = root / "Knight-Client/Textures/Ninja Adventure - Asset Pack/Ui/Font"
    source = directory / "fusion-pixel-10px-monospaced-zh_hans.ttf"
    destination = directory / "KnightCjk10-NoAscii.ttf"
    font = TTFont(source)
    retained = {code for code in font.getBestCmap() if code >= 0x80}
    options = subset.Options()
    options.name_IDs = ["*"]
    options.name_languages = ["*"]
    options.name_legacy = True
    options.layout_features = ["*"]
    worker = subset.Subsetter(options=options)
    worker.populate(unicodes=retained)
    worker.subset(font)
    # Identify the derivative separately, retaining source copyright and license records.
    replacements = {1: "Knight CJK 10", 2: "Regular", 3: "KnightCJK10-NoAscii",
                    4: "Knight CJK 10 Regular", 6: "KnightCJK10-NoAscii",
                    16: "Knight CJK 10", 17: "Regular"}
    for record in font["name"].names:
        if record.nameID in replacements:
            record.string = replacements[record.nameID].encode(record.getEncoding())
    font.save(destination)
    with TTFont(destination) as result:
        for table in result["cmap"].tables:
            if table.isUnicode():
                assert not any(code < 0x80 for code in table.cmap)
        assert set(result.getBestCmap()) == retained
    print(f"Built {destination.name}: {len(retained)} non-ASCII codepoints; ASCII removed.")


if __name__ == "__main__":
    main()
