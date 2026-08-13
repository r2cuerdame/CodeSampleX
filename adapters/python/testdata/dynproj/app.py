import fastapi
from fastapi import FastAPI

thing = getattr(fastapi, "FastAPI")
app = thing()
