"""Press the browser's native extension action in the marked test window."""
import sys
import time
import warnings

import gi

gi.require_version("Atspi", "2.0")
from gi.repository import Atspi

warnings.filterwarnings("ignore", category=DeprecationWarning)
Atspi.init()


def children(node):
    for index in range(node.get_child_count()):
        child = node.get_child_at_index(index)
        if child is not None:
            yield child


def browser_controls(node):
    yield node
    # Web content must never impersonate a browser toolbar action.
    if node.get_role_name() in {"document web", "document frame", "entry", "menu", "menu item"}:
        return
    for child in children(node):
        yield from browser_controls(child)


def press(node):
    action = node.get_action_iface()
    if action is None:
        return False
    for i in range(action.get_n_actions()):
        if action.get_action_name(i) in {"click", "press", "activate", "open"}:
            return action.do_action(i)
    return False


marker = sys.argv[1]
deadline = time.monotonic() + 8
while time.monotonic() < deadline:
    for app in children(Atspi.get_desktop(0)):
        frames = list(children(app))
        marked = next((f for f in frames if f.get_role_name() == "frame" and marker in f.get_name()), None)
        if marked is None:
            continue
        for button in browser_controls(marked):
            if (button.get_role_name() in {"button", "push button"}
                    and button.get_name() == "登录 GPT"
                    and button.get_state_set().contains(Atspi.StateType.SHOWING)
                    and press(button)):
                sys.exit(0)
    time.sleep(0.15)
sys.exit("未能在指定测试窗口的地址栏旁找到可操作的「登录 GPT」按钮")
