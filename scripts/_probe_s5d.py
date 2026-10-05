#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""scripts/_probe_s5d.py —— 验证 **`S5d` 在两侧引擎都真的生效**（不是"清单里写了但没人执行"）。

★ 为什么需要它：「清单一绿」不能证明**新判据被执行** —— 判据可能因为
  **名字写错 / 原语未实现 / 被静默跳过** 而从未运行，而门禁依旧全绿（`N-011` 要防的漂移）。
★ 做法（**改文件，不改内存** —— `N-026` 立的规矩）：
  1. 备份 `spec/forms/BA.json`（**在内存 + 落盘各一份**，并记 `sha256`）；
  2. 篡改：删掉它顶层的 `"ledger"` 键（**只删这一行**）；
  3. 断言 **Python 侧**（`scripts/check_spec.py`）**必须**报 `S5d`；
  4. 断言 **Go 侧**（`go test ./internal/specload/`）**必须**转红 —— 两侧都拦才算「共同契约」；
  5. **还原并逐字节校验**（`sha256` 不一致 ⇒ 探针自身失败，绝不静默放过）；
  6. 反向控制：还原后两侧都必须**复绿**（排除「本来就红」）。

用法：python scripts/_probe_s5d.py        退出码 0 = 六项全如期；1 = 至少一项不符
"""
import hashlib
import pathlib
import re
import subprocess
import tempfile
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
TARGET = ROOT / "spec" / "forms" / "BA.json"
BAK = pathlib.Path(tempfile.gettempdir()) / "_probe_s5d_BA.json"  # N-060 T3: 不落仓

results = []


def sh(cmd):
    p = subprocess.run(cmd, cwd=str(ROOT), capture_output=True, text=True, shell=False)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def record(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("  ✓ " if ok else "  ✗ ") + name + (f"   {detail}" if detail else ""))


def restore(orig: bytes) -> bool:
    TARGET.write_bytes(orig)
    return hashlib.sha256(TARGET.read_bytes()).hexdigest() == hashlib.sha256(orig).hexdigest()


def main() -> int:
    orig = TARGET.read_bytes()
    h = hashlib.sha256(orig).hexdigest()
    BAK.parent.mkdir(parents=True, exist_ok=True)
    BAK.write_bytes(orig)

    text = orig.decode("utf-8")
    # 篡改：删掉顶层 "ledger" 那一行
    new_text, n = re.subn(r'^\s*"ledger":\s*\[[^\]]*\],\s*$', "", text, count=1, flags=re.M)
    if n != 1:
        print("★ 探针自身失败：未命中 `\"ledger\"` 行（目标文件的排版可能已变）")
        return 1

    try:
        TARGET.write_bytes(new_text.encode("utf-8"))

        rc_py, out_py = sh([sys.executable, "scripts/check_spec.py"])
        record("篡改后 Python 侧必须报 S5d", rc_py != 0 and "S5d" in out_py,
               f"rc={rc_py}")

        rc_go, out_go = sh(["go", "test", "./internal/specload/", "-count=1"])
        record("篡改后 Go 侧必须转红（两侧共同契约）", rc_go != 0,
               f"rc={rc_go}")
        record("★ Go 侧报的是 S5d（不是别的错）", "S5d" in out_go,
               "（若不含 S5d，说明 Go 只是巧合红了）")
    finally:
        ok = restore(orig)
        record("还原可证明（sha256 逐字节一致）", ok, h[:12])
        if not ok:
            print("★★ 还原失败 —— 请手工 `git checkout -- spec/forms/BA.json`！")
            return 1

    rc_py2, out_py2 = sh([sys.executable, "scripts/check_spec.py"])
    record("反向控制：还原后 Python 侧复绿（排除「本来就红」）", rc_py2 == 0, f"rc={rc_py2}")
    rc_go2, _ = sh(["go", "test", "./internal/specload/", "-count=1"])
    record("反向控制：还原后 Go 侧复绿", rc_go2 == 0, f"rc={rc_go2}")

    bad = [r for r in results if not r[1]]
    print(f"\n{'★ 全部如期' if not bad else '★ 有 %d 项不符' % len(bad)}（共 {len(results)} 项）")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
