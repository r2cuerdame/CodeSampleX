import yaml
from fastapi import FastAPI, APIRouter

app = FastAPI()
router = APIRouter()


@router.get("/config")
def config() -> dict:
    with open("config.yaml") as f:
        return yaml.safe_load(f)


app.include_router(router)
