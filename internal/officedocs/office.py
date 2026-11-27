#!/usr/bin/env python3
"""MCPshka document worker: one bounded run over one document.

Reads a JSON spec, drives a private headless LibreOffice through UNO and
prints one JSON result line. Document content is UNTRUSTED data.

spec: {id, action: inspect|edit, path, output?, workdir, sheet?, range?,
       max_cells?, macros?, ops?: [...], script?, preview?: bool}
"""
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import time
import traceback

TEXT_EXT = {".txt", ".md", ".json", ".xml", ".yaml", ".yml", ".ini", ".conf", ".log", ".py", ".sh", ".bas", ".vbs", ".sql", ".tsv"}
FILTERS = {
    "calc": {".ods": "calc8", ".xlsx": "Calc MS Excel 2007 XML", ".xlsm": "Calc MS Excel 2007 VBA XML", ".xls": "MS Excel 97",
             ".csv": "Text - txt - csv (StarCalc)", ".pdf": "calc_pdf_Export", ".html": "HTML (StarCalc)", ".png": "calc_png_Export",
             ".fods": "OpenDocument Spreadsheet Flat XML", ".ots": "calc8_template"},
    "writer": {".odt": "writer8", ".docx": "MS Word 2007 XML", ".docm": "MS Word 2007 XML VBA", ".doc": "MS Word 97",
               ".rtf": "Rich Text Format", ".txt": "Text (encoded)", ".pdf": "writer_pdf_Export", ".html": "HTML (StarWriter)",
               ".png": "writer_png_Export", ".fodt": "OpenDocument Text Flat XML", ".ott": "writer8_template"},
    "impress": {".odp": "impress8", ".pptx": "Impress MS PowerPoint 2007 XML", ".ppt": "MS PowerPoint 97",
                ".pdf": "impress_pdf_Export", ".png": "impress_png_Export"},
    "draw": {".odg": "draw8", ".pdf": "draw_pdf_Export", ".png": "draw_png_Export"},
}
NEW_DOC = {".ods": "scalc", ".xlsx": "scalc", ".xlsm": "scalc", ".xls": "scalc", ".csv": "scalc",
           ".odt": "swriter", ".docx": "swriter", ".docm": "swriter", ".doc": "swriter", ".rtf": "swriter",
           ".odp": "simpress", ".pptx": "simpress", ".odg": "sdraw"}
CSV_OPTS = "44,34,76,1,,0,false,true,true"  # comma, quote, UTF-8, from row 1


class Fail(Exception):
    pass


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def color(value):
    if isinstance(value, int):
        return value
    m = re.fullmatch(r"#?([0-9a-fA-F]{6})", str(value))
    if not m:
        raise Fail("color must be #RRGGBB: %r" % (value,))
    return int(m.group(1), 16)


# ---------- plain text files: no LibreOffice needed ----------

def text_run(spec, res):
    path = spec["path"]
    text = ""
    if os.path.exists(path):
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            text = f.read()
    res["kind"] = "text"
    done = []
    for op in spec.get("ops") or []:
        name = op.get("op")
        if name == "set_text":
            text = str(op.get("text", ""))
        elif name == "append":
            text += ("" if text.endswith("\n") or not text else "\n") + str(op.get("text", "")) + "\n"
        elif name == "replace":
            if op.get("regex"):
                text, n = re.subn(op["find"], op.get("replace", ""), text, flags=re.M)
            else:
                n = text.count(op["find"])
                text = text.replace(op["find"], op.get("replace", ""))
            done.append({"op": name, "replaced": n})
            continue
        elif name == "set_lines":  # {at: 1-based line, count, lines: [...]}
            lines = text.split("\n")
            at = int(op.get("at", len(lines) + 1)) - 1
            lines[at:at + int(op.get("count", 0))] = [str(x) for x in op.get("lines", [])]
            text = "\n".join(lines)
        else:
            raise Fail("text files take ops set_text, append, replace, set_lines; got %r" % (name,))
        done.append({"op": name})
    if spec["action"] == "edit":
        out = spec["output"]
        with open(out, "w", encoding="utf-8") as f:
            f.write(text)
        res["ops"] = done
        res["output"] = {"path": out, "sha256": sha256(out), "size": os.path.getsize(out)}
    limit = int(spec.get("max_cells") or 2000) * 40
    lines = text.split("\n")
    res["text"] = {"lines": len(lines), "chars": len(text), "content": text[:limit], "truncated": len(text) > limit}


# ---------- LibreOffice ----------

class Office:
    def __init__(self, spec):
        import uno  # noqa: F401  (python3-uno)
        self.uno = uno
        self.spec = spec
        self.pipe = "mcpshka" + spec["id"]
        profile = os.path.join(spec["workdir"], ".lo-profile")
        os.makedirs(profile, exist_ok=True)
        self.proc = subprocess.Popen(
            ["/usr/bin/soffice", "--headless", "--invisible", "--norestore", "--nologo", "--nodefault", "--nolockcheck",
             "--nofirststartwizard", "-env:UserInstallation=file://" + profile,
             "--accept=pipe,name=%s;urp;StarOffice.ComponentContext" % self.pipe],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=dict(os.environ, HOME=spec["workdir"]))
        local = uno.getComponentContext()
        resolver = local.ServiceManager.createInstanceWithContext("com.sun.star.bridge.UnoUrlResolver", local)
        deadline = time.time() + 90
        while True:
            try:
                self.ctx = resolver.resolve("uno:pipe,name=%s;urp;StarOffice.ComponentContext" % self.pipe)
                break
            except Exception:
                if time.time() > deadline or self.proc.poll() is not None:
                    raise Fail("LibreOffice did not start (soffice exit %s)" % self.proc.poll())
                time.sleep(0.5)
        self.smgr = self.ctx.ServiceManager
        self.desktop = self.smgr.createInstanceWithContext("com.sun.star.frame.Desktop", self.ctx)
        self.doc = None

    def prop(self, name, value):
        p = self.uno.createUnoStruct("com.sun.star.beans.PropertyValue")
        p.Name, p.Value = name, value
        return p

    def url(self, path):
        return self.uno.systemPathToFileUrl(os.path.abspath(path))

    def open(self, path, run_macros=False):
        ext = os.path.splitext(path)[1].lower()
        if not os.path.exists(path):
            kind = NEW_DOC.get(ext)
            if not kind:
                raise Fail("no such file, and %r cannot be created empty" % ext)
            self.doc = self.desktop.loadComponentFromURL("private:factory/" + kind, "_blank", 0, (self.prop("Hidden", True),))
        else:
            args = [self.prop("Hidden", True), self.prop("MacroExecutionMode", 4 if run_macros else 0), self.prop("UpdateDocMode", 0)]
            if ext in (".csv", ".tsv"):
                args += [self.prop("FilterName", "Text - txt - csv (StarCalc)"), self.prop("FilterOptions", CSV_OPTS if ext == ".csv" else "9,34,76,1")]
            self.doc = self.desktop.loadComponentFromURL(self.url(path), "_blank", 0, tuple(args))
        if self.doc is None:
            raise Fail("LibreOffice could not open the file (unknown or damaged format)")
        return self.doc

    def kind(self):
        d = self.doc
        for service, kind in (("com.sun.star.sheet.SpreadsheetDocument", "calc"), ("com.sun.star.text.TextDocument", "writer"),
                              ("com.sun.star.presentation.PresentationDocument", "impress"), ("com.sun.star.drawing.DrawingDocument", "draw")):
            if d.supportsService(service):
                return kind
        raise Fail("unsupported document type")

    def save(self, out):
        ext = os.path.splitext(out)[1].lower()
        flt = FILTERS[self.kind()].get(ext)
        if not flt:
            raise Fail("cannot save a %s document as %s; allowed: %s" % (self.kind(), ext, " ".join(sorted(FILTERS[self.kind()]))))
        args = [self.prop("FilterName", flt), self.prop("Overwrite", True)]
        if ext == ".csv":
            args.append(self.prop("FilterOptions", CSV_OPTS))
        if ext == ".txt":
            args.append(self.prop("FilterOptions", "UTF8"))
        self.doc.storeToURL(self.url(out), tuple(args))
        if not os.path.exists(out):
            raise Fail("LibreOffice wrote no file for %s" % ext)

    def close(self):
        try:
            if self.doc is not None:
                self.doc.close(True)
        except Exception:
            pass
        try:
            self.desktop.terminate()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=15)
        except Exception:
            self.proc.kill()

    # ----- Calc -----
    def sheet(self, name=None):
        sheets = self.doc.Sheets
        if name in (None, ""):
            return self.doc.CurrentController.ActiveSheet if self.doc.CurrentController else sheets.getByIndex(0)
        if isinstance(name, int):
            return sheets.getByIndex(name)
        if not sheets.hasByName(name):
            raise Fail("no sheet %r; sheets: %s" % (name, ", ".join(sheets.ElementNames)))
        return sheets.getByName(name)

    def used(self, sh):
        cur = sh.createCursor()
        cur.gotoEndOfUsedArea(False)
        return cur.RangeAddress.EndRow, cur.RangeAddress.EndColumn

    def inspect_calc(self, res):
        spec = self.spec
        budget = int(spec.get("max_cells") or 2000)
        out = []
        for name in self.doc.Sheets.ElementNames:
            sh = self.doc.Sheets.getByName(name)
            rows, cols = self.used(sh)
            info = {"name": name, "used": "A1:" + addr(rows, cols), "rows": rows + 1, "cols": cols + 1}
            if spec.get("sheet") in (None, "", name) and budget > 0:
                rng = sh.getCellRangeByName(spec["range"]) if spec.get("range") else sh.getCellRangeByPosition(0, 0, cols, rows)
                a = rng.RangeAddress
                cells, n = [], 0
                formulas, data = rng.getFormulaArray(), rng.getDataArray()
                for r, (frow, drow) in enumerate(zip(formulas, data)):
                    for c, (f, v) in enumerate(zip(frow, drow)):
                        if f == "" or n >= budget:
                            continue
                        cell = {"a": addr(a.StartRow + r, a.StartColumn + c), "v": v}
                        if isinstance(f, str) and f.startswith("="):
                            cell["f"] = f
                        cells.append(cell)
                        n += 1
                budget -= n
                info["cells"] = cells
                info["truncated"] = n >= int(spec.get("max_cells") or 2000)
            out.append(info)
        res["sheets"] = out

    # ----- Writer -----
    def paragraphs(self):
        items, en = [], self.doc.Text.createEnumeration()
        while en.hasMoreElements():
            items.append(en.nextElement())
        return items

    def inspect_writer(self, res):
        budget = int(self.spec.get("max_cells") or 2000)
        paras, tables = [], 0
        for i, p in enumerate(self.paragraphs()):
            if p.supportsService("com.sun.star.text.TextTable"):
                tables += 1
                if len(paras) < budget:
                    paras.append({"i": i, "table": p.Name, "rows": p.Rows.Count, "cols": p.Columns.Count,
                                  "cells": table_cells(p)})
                continue
            if len(paras) < budget and (p.String or p.ParaStyleName.startswith("Heading")):
                paras.append({"i": i, "style": p.ParaStyleName, "text": p.String[:2000]})
        res["text"] = {"paragraphs": paras, "tables": tables, "pages": getattr(self.doc.CurrentController, "PageCount", None)}

    # ----- macros -----
    def macros(self, with_source=True):
        out = []
        libs = self.doc.BasicLibraries
        for lib in libs.ElementNames:
            if not libs.isLibraryLoaded(lib):
                libs.loadLibrary(lib)
            mods = libs.getByName(lib)
            for mod in mods.ElementNames:
                src = mods.getByName(mod)
                item = {"library": lib, "module": mod, "language": "basic", "lines": src.count("\n") + 1}
                if with_source:
                    item["source"] = src[:60000]
                out.append(item)
        return out


def table_cells(table):
    """Text of a Writer table, first 50 rows (merged cells make it ragged)."""
    rows = []
    for name in table.CellNames:
        m = re.fullmatch(r"([A-Za-z]+)(\d+)(\..*)?", name)
        r = int(m.group(2)) - 1 if m else 0
        if r >= 50:
            continue
        while len(rows) <= r:
            rows.append([])
        rows[r].append(table.getCellByName(name).String[:500])
    return rows


def addr(row, col):
    s = ""
    col += 1
    while col:
        col, rem = divmod(col - 1, 26)
        s = chr(65 + rem) + s
    return "%s%d" % (s, row + 1)


def fmt_props(o, target, op):
    """Character/cell formatting shared by Calc ranges and Writer text."""
    from com.sun.star.awt.FontWeight import BOLD, NORMAL
    from com.sun.star.awt.FontSlant import ITALIC, NONE
    if "bold" in op:
        target.CharWeight = BOLD if op["bold"] else NORMAL
    if "italic" in op:
        target.CharPosture = ITALIC if op["italic"] else NONE
    if "underline" in op:
        target.CharUnderline = 1 if op["underline"] else 0
    if "font" in op:
        target.CharFontName = op["font"]
    if "size" in op:
        target.CharHeight = float(op["size"])
    if "color" in op:
        target.CharColor = color(op["color"])


def op_calc(o, op):
    name = op["op"]
    doc = o.doc
    if name == "add_sheet":
        doc.Sheets.insertNewByName(op["name"], int(op.get("position", doc.Sheets.Count)))
        return {}
    if name == "remove_sheet":
        doc.Sheets.removeByName(op["sheet"])
        return {}
    if name == "rename_sheet":
        o.sheet(op["sheet"]).Name = op["name"]
        return {}
    if name == "copy_sheet":
        doc.Sheets.copyByName(op["sheet"], op["name"], int(op.get("position", doc.Sheets.Count)))
        return {}
    sh = o.sheet(op.get("sheet"))
    if name == "set_cells":
        rows = op.get("rows") or []
        if not rows:
            raise Fail("set_cells needs rows: [[...], ...]")
        width = max(len(r) for r in rows)
        start = sh.getCellRangeByName(op.get("start", "A1")).RangeAddress
        rng = sh.getCellRangeByPosition(start.StartColumn, start.StartRow, start.StartColumn + width - 1, start.StartRow + len(rows) - 1)
        current = rng.getFormulaArray()
        data = []
        for r, row in enumerate(rows):
            line = []
            for c in range(width):
                v = row[c] if c < len(row) else None
                if v is None:
                    v = current[r][c]
                elif isinstance(v, bool):
                    v = "=TRUE()" if v else "=FALSE()"
                elif isinstance(v, (int, float)):
                    v = repr(v) if isinstance(v, float) else str(v)
                else:
                    v = str(v)
                line.append(v)
            data.append(tuple(line))
        rng.setFormulaArray(tuple(data))
        return {"range": rng.AbsoluteName}
    if name == "clear":
        sh.getCellRangeByName(op["range"]).clearContents(1 | 2 | 4 | 16 | (32 if op.get("formats") else 0))
        return {}
    if name == "format":
        rng = sh.getCellRangeByName(op["range"])
        fmt_props(o, rng, op)
        if "background" in op:
            rng.CellBackColor = color(op["background"])
        if "align" in op:
            rng.HoriJustify = {"left": 1, "center": 2, "right": 3, "justify": 4}[op["align"]]
        if "valign" in op:
            rng.VertJustify = {"top": 1, "middle": 2, "center": 2, "bottom": 3}[op["valign"]]
        if "wrap" in op:
            rng.IsTextWrapped = bool(op["wrap"])
        if "number_format" in op:
            fmts = doc.NumberFormats
            loc = o.uno.createUnoStruct("com.sun.star.lang.Locale")
            key = fmts.queryKey(op["number_format"], loc, False)
            rng.NumberFormat = key if key != -1 else fmts.addNew(op["number_format"], loc)
        if op.get("border"):
            line = o.uno.createUnoStruct("com.sun.star.table.BorderLine2")
            line.OuterLineWidth, line.Color = int(op.get("border_width", 18)), color(op.get("border_color", "#000000"))
            tb = rng.TableBorder
            for side in ("TopLine", "BottomLine", "LeftLine", "RightLine", "HorizontalLine", "VerticalLine"):
                setattr(tb, side, line)
                setattr(tb, "Is" + side + "Valid", True)
            rng.TableBorder = tb
        if "merge" in op:
            rng.merge(bool(op["merge"]))
        return {}
    if name == "col_width":
        cols = sh.getCellRangeByName(op["columns"] if ":" in op["columns"] else op["columns"] + ":" + op["columns"]).Columns
        if op.get("optimal"):
            cols.OptimalWidth = True
        else:
            cols.Width = int(float(op["width_mm"]) * 100)
        return {}
    if name == "row_height":
        rows = sh.getCellRangeByName(op["rows"] if ":" in str(op["rows"]) else "%s:%s" % (op["rows"], op["rows"])).Rows
        if op.get("optimal"):
            rows.OptimalHeight = True
        else:
            rows.Height = int(float(op["height_mm"]) * 100)
        return {}
    if name in ("insert_rows", "delete_rows", "insert_columns", "delete_columns"):
        coll = sh.Rows if name.endswith("rows") else sh.Columns
        at, count = int(op["at"]) - 1, int(op.get("count", 1))
        (coll.insertByIndex if name.startswith("insert") else coll.removeByIndex)(at, count)
        return {}
    if name == "freeze":
        doc.CurrentController.setActiveSheet(sh)
        doc.CurrentController.freezeAtPosition(int(op.get("cols", 0)), int(op.get("rows", 0)))
        return {}
    if name == "autofilter":
        ranges = doc.DatabaseRanges
        nm = op.get("name", "mcpshka_filter_" + re.sub(r"\W", "_", sh.Name))
        if ranges.hasByName(nm):
            ranges.removeByName(nm)
        ranges.addNewByName(nm, sh.getCellRangeByName(op["range"]).RangeAddress)
        ranges.getByName(nm).AutoFilter = True
        return {}
    if name == "chart":
        rng = sh.getCellRangeByName(op["range"]).RangeAddress
        rect = o.uno.createUnoStruct("com.sun.star.awt.Rectangle")
        rect.X, rect.Y = int(op.get("x_mm", 10) * 100), int(op.get("y_mm", 10) * 100)
        rect.Width, rect.Height = int(op.get("width_mm", 140) * 100), int(op.get("height_mm", 80) * 100)
        nm = op.get("name", "Chart%d" % (sh.Charts.Count + 1))
        sh.Charts.addNewByName(nm, rect, (rng,), bool(op.get("col_headers", True)), bool(op.get("row_headers", True)))
        chart = sh.Charts.getByName(nm).EmbeddedObject
        kinds = {"bar": "BarDiagram", "line": "LineDiagram", "pie": "PieDiagram", "area": "AreaDiagram", "xy": "XYDiagram"}
        chart.Diagram = chart.createInstance("com.sun.star.chart." + kinds[op.get("type", "bar")])
        if op.get("title"):
            chart.HasMainTitle = True
            chart.Title.String = op["title"]
        return {"chart": nm}
    raise Fail("unknown spreadsheet op %r; built in: %s, macro_set, macro_remove, macro_run, bind_event — anything else through script" % (name, CALC_OPS))


def para_style(doc, name):
    """Paragraph style by its programmatic or displayed name."""
    styles = doc.StyleFamilies.getByName("ParagraphStyles")
    if styles.hasByName(name):
        return name
    for n in styles.ElementNames:
        if styles.getByName(n).DisplayName == name:
            return n
    raise Fail("no paragraph style %r (e.g. Standard, Heading 1, Title, Text Body, List Bullet)" % name)


def op_writer(o, op):
    name = op["op"]
    doc = o.doc
    text = doc.Text
    if name == "append":
        from com.sun.star.text.ControlCharacter import PARAGRAPH_BREAK
        cur = text.createTextCursor()
        cur.gotoEnd(False)
        if text.String:
            text.insertControlCharacter(cur, PARAGRAPH_BREAK, False)
        if op.get("page_break"):
            from com.sun.star.style.BreakType import PAGE_BEFORE
            cur.BreakType = PAGE_BEFORE
        # Insert first, then style the new paragraph itself: a collapsed
        # cursor would carry the previous paragraph's direct formatting on.
        text.insertString(cur, str(op.get("text", "")), False)
        para = o.paragraphs()[-1]
        para.ParaStyleName = para_style(doc, op.get("style", "Standard"))
        for attr in ("CharWeight", "CharPosture", "CharUnderline", "CharHeight", "CharColor", "ParaAdjust"):
            try:
                para.setPropertyToDefault(attr)
            except Exception:
                pass
        fmt_props(o, para, op)
        if "align" in op:
            para.ParaAdjust = {"left": 0, "right": 1, "justify": 2, "center": 3}[op["align"]]
        return {}
    if name == "replace":
        d = doc.createReplaceDescriptor()
        d.SearchString, d.ReplaceString = op["find"], op.get("replace", "")
        d.SearchRegularExpression = bool(op.get("regex"))
        d.SearchCaseSensitive = bool(op.get("case", True))
        return {"replaced": doc.replaceAll(d)}
    if name == "set_paragraph":
        p = o.paragraphs()[int(op["index"])]
        if "text" in op:
            p.String = str(op["text"])
        if "style" in op:
            p.ParaStyleName = para_style(doc, op["style"])
        fmt_props(o, p, op)
        if "align" in op:
            p.ParaAdjust = {"left": 0, "right": 1, "justify": 2, "center": 3}[op["align"]]
        return {}
    if name == "delete_paragraph":
        o.paragraphs()[int(op["index"])].dispose()
        return {}
    if name == "insert_table":
        rows = op.get("rows") or []
        if not rows:
            raise Fail("insert_table needs rows: [[...], ...]")
        width = max(len(r) for r in rows)
        table = doc.createInstance("com.sun.star.text.TextTable")
        table.initialize(len(rows), width)
        cur = text.createTextCursor()
        cur.gotoEnd(False)
        text.insertTextContent(cur, table, False)
        for r, row in enumerate(rows):
            for c in range(width):
                cell = table.getCellByPosition(c, r)
                cell.String = "" if c >= len(row) or row[c] is None else str(row[c])
                if r == 0 and op.get("header", True):
                    tc = cell.createTextCursor()
                    tc.gotoEnd(True)
                    tc.CharWeight = 150.0
        return {"table": table.Name}
    if name == "page":
        pages = doc.StyleFamilies.getByName("PageStyles")
        nm = op.get("style", "Standard")
        style = pages.getByName(nm if pages.hasByName(nm) else pages.ElementNames[0])
        if op.get("landscape") is not None and bool(op["landscape"]) != style.IsLandscape:
            style.IsLandscape = bool(op["landscape"])
            style.Width, style.Height = style.Height, style.Width
        for key, prop in (("margin_left_mm", "LeftMargin"), ("margin_right_mm", "RightMargin"), ("margin_top_mm", "TopMargin"), ("margin_bottom_mm", "BottomMargin")):
            if key in op:
                setattr(style, prop, int(float(op[key]) * 100))
        return {}
    raise Fail("unknown text document op %r; built in: %s, macro_set, macro_remove, macro_run, bind_event — anything else through script" % (name, WRITER_OPS))


def op_macro(o, op):
    name = op["op"]
    doc = o.doc
    libs = doc.BasicLibraries
    lib = op.get("library", "Standard")
    if name == "macro_set":
        if not libs.hasByName(lib):
            libs.createLibrary(lib)
        if not libs.isLibraryLoaded(lib):
            libs.loadLibrary(lib)
        mods = libs.getByName(lib)
        source = str(op["source"])
        if op.get("vba") and "Option VBASupport" not in source:
            source = "Option VBASupport 1\n" + source
        if mods.hasByName(op["module"]):
            mods.replaceByName(op["module"], source)
        else:
            mods.insertByName(op["module"], source)
        return {"library": lib, "module": op["module"], "lines": source.count("\n") + 1}
    if name == "macro_remove":
        libs.getByName(lib).removeByName(op["module"])
        return {}
    if name == "macro_run":
        # "Standard.Module1.Main" → the document's own Basic script.
        uri = "vnd.sun.star.script:%s?language=Basic&location=document" % op["name"]
        script = doc.getScriptProvider().getScript(uri)
        out = script.invoke(tuple(op.get("args", ())), (), ())
        value = out[0] if isinstance(out, tuple) else out
        return {"returned": value if isinstance(value, (int, float, str, bool, type(None))) else str(value)}
    if name == "bind_event":
        # {event: OnLoad|OnSave|OnPrepareUnload|…, macro: "Standard.Module1.Main"}
        uri = "vnd.sun.star.script:%s?language=Basic&location=document" % op["macro"]
        if not doc.Events.hasByName(op["event"]):
            raise Fail("no document event %r; events: %s" % (op["event"], ", ".join(doc.Events.ElementNames)))
        props = o.uno.Any("[]com.sun.star.beans.PropertyValue", (o.prop("EventType", "Script"), o.prop("Script", uri)))
        o.uno.invoke(doc.Events, "replaceByName", (op["event"], props))
        return {}
    raise Fail("unknown macro op %r" % name)


CALC_OPS = "set_cells, clear, format, col_width, row_height, insert_rows, delete_rows, insert_columns, delete_columns, add_sheet, rename_sheet, copy_sheet, remove_sheet, freeze, autofilter, chart"
WRITER_OPS = "append, replace, set_paragraph, delete_paragraph, insert_table, page"
# What a caller is likely to write instead of the built-in name.
ALIASES = {"paragraph": "append", "text": "append", "add_paragraph": "append", "heading": "append",
           "table": "insert_table", "add_table": "insert_table",
           "write": "set_cells", "set_values": "set_cells", "set_range": "set_cells", "cells": "set_cells"}


def normalize(op):
    """Accept the obvious spellings: the caller works towards a result, not a syntax."""
    name = op.get("op", "")
    if name == "heading":
        op.setdefault("style", "Heading %d" % int(op.get("level", 1)))
    op["op"] = name = ALIASES.get(name, name)
    if name in ("set_cells", "insert_table") and not op.get("rows"):
        op["rows"] = op.get("values") or op.get("data") or op.get("cells") or []
    if name == "set_cells" and "start" not in op and op.get("range"):
        op["start"] = op["range"]
    return name


def run_ops(o, res):
    kind = o.kind()
    done = []
    for i, op in enumerate(o.spec.get("ops") or []):
        name = normalize(op)
        try:
            if name.startswith("macro_") or name == "bind_event":
                detail = op_macro(o, op)
            elif kind == "calc":
                detail = op_calc(o, op)
            elif kind == "writer":
                detail = op_writer(o, op)
            else:
                raise Fail("ops for %s documents are not built in: use script" % kind)
        except Fail:
            raise
        except Exception as exc:
            raise Fail("op %d (%s) failed: %s" % (i, name, str(exc)[:300] or type(exc).__name__))
        done.append(dict(detail or {}, op=name))
    res["ops"] = done


def run_script(o, res):
    """Escape hatch: Python-UNO code with the open document in scope."""
    scope = {"doc": o.doc, "office": o, "ctx": o.ctx, "smgr": o.smgr, "desktop": o.desktop, "uno": o.uno,
             "prop": o.prop, "sheet": o.sheet, "color": color, "result": {}, "Fail": Fail}
    try:
        exec(compile(o.spec["script"], "<doc_edit script>", "exec"), scope)
    except Fail:
        raise
    except Exception:
        tb = traceback.format_exc().splitlines()
        raise Fail("script failed: " + " | ".join(tb[-4:])[:600])
    try:
        res["script_result"] = json.loads(json.dumps(scope.get("result"), default=str))
    except Exception:
        res["script_result"] = str(scope.get("result"))[:2000]


def office_run(spec, res):
    o = Office(spec)
    try:
        run_macros = any((op.get("op") == "macro_run") for op in spec.get("ops") or [])
        o.open(spec["path"], run_macros)
        kind = res["kind"] = o.kind()
        if spec["action"] == "edit":
            run_ops(o, res)
            if spec.get("script"):
                run_script(o, res)
            out = spec["output"]
            o.save(out)
            odf = os.path.splitext(out)[1].lower() in (".ods", ".odt", ".odp", ".odg", ".fods", ".fodt", ".ots", ".ott")
            if not odf and any(str(op.get("op", "")).startswith("macro_set") or op.get("op") == "bind_event" for op in spec.get("ops") or []):
                res["warnings"] = ["LibreOffice writes new or changed macros only into OpenDocument files (.ods/.odt/.odp); "
                                   "in MS Office formats existing VBA is kept as it was. Save as .ods/.odt to ship this macro."]
            res["output"] = {"path": out, "sha256": sha256(out), "size": os.path.getsize(out)}
        if spec.get("preview"):
            png = os.path.join(spec["workdir"], "preview.png")
            try:
                if kind == "calc" and spec.get("sheet"):
                    o.doc.CurrentController.setActiveSheet(o.sheet(spec["sheet"]))
                o.doc.storeToURL(o.url(png), (o.prop("FilterName", FILTERS[kind][".png"]), o.prop("Overwrite", True)))
                res["preview"] = {"path": png, "size": os.path.getsize(png)}
            except Exception as exc:
                res["preview"] = {"error": str(exc)[:200]}
        if kind == "calc":
            o.inspect_calc(res)
        elif kind == "writer":
            o.inspect_writer(res)
        else:
            pages = o.doc.DrawPages
            res["slides"] = [{"i": i, "name": pages.getByIndex(i).Name,
                              "text": [s.String[:500] for s in (pages.getByIndex(i).getByIndex(k) for k in range(pages.getByIndex(i).Count)) if hasattr(s, "String") and s.String]}
                             for i in range(pages.Count)]
        try:
            res["macros"] = o.macros(spec.get("macros", True))
        except Exception as exc:
            res["macros_error"] = str(exc)[:200]
    finally:
        o.close()


def main():
    res = {"schema": "mcpshka.doc/v1", "trust": "UNTRUSTED", "state": "failed"}
    spec = {}
    try:
        with open(sys.argv[1], "r", encoding="utf-8") as f:
            spec = json.load(f)
        res["id"], res["action"] = spec["id"], spec["action"]
        os.makedirs(spec["workdir"], exist_ok=True)
        if spec["action"] == "edit" and not spec.get("output"):
            raise Fail("output path required")
        src_ext = os.path.splitext(spec["path"])[1].lower()
        out_ext = os.path.splitext(spec.get("output") or spec["path"])[1].lower()
        if src_ext in TEXT_EXT and out_ext in TEXT_EXT and not spec.get("script") and not spec.get("preview"):
            text_run(spec, res)
        else:
            office_run(spec, res)
        res["state"] = "done"
    except Fail as exc:
        res["error"] = str(exc)
    except Exception as exc:
        res["error"] = "%s: %s" % (type(exc).__name__, str(exc)[:400])
    if spec.get("workdir"):
        shutil.rmtree(os.path.join(spec["workdir"], ".lo-profile"), ignore_errors=True)
    print(json.dumps(res, ensure_ascii=False))
    return 0 if res["state"] == "done" else 1


if __name__ == "__main__":
    sys.exit(main())
