import os
from fastapi import FastAPI
import psycopg2
import boto3
from urllib.parse import urlparse

app = FastAPI()

@app.get("/")
def read_root():
    return {"message": "Welcome to your FastAPI app powered by Score!"}

@app.get("/db-check")
def db_check():
    conStr = os.getenv("PG_CONNECTION_STRING")
    p = urlparse(conStr)
    pg_connection_dict = {
        'dbname': p.path[1:],
        'user': p.username,
        'password': p.password,
        'port': p.port,
        'host': p.hostname
    }
    con = psycopg2.connect(**pg_connection_dict)
    cur = con.cursor()
    cur.execute("SELECT 1;")
    result = cur.fetchone()
    con.close()
    return {"db_status": "Connected ✅"}

@app.get("/bucket-check")
def bucket_check():
    s3 = boto3.client(
            "s3",
            region_name=os.getenv("BUCKET_REGION")
        )
    bucket = os.getenv("BUCKET_NAME")
    response = s3.list_objects_v2(Bucket=bucket)
    print(response)
    return {"bucket_status": "Accessible ✅", "objects": response.get("Contents", [])}
