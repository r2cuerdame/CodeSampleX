import importlib

from yaml import safe_load

mod = importlib.import_module("yaml")


def load(text):
    return safe_load(text)
