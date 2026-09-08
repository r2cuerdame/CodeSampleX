import { chromium } from 'playwright';
import fs from 'node:fs';
import assert from 'node:assert/strict';
const root = process.env.CSX_ACCEPTANCE_ROOT || '/workspace/acceptance';
const roomId = process.env.CSX_ACCEPTANCE_ROOM_ID;
assert.ok(roomId,'CSX_ACCEPTANCE_ROOM_ID is required');
const fixture = JSON.parse(fs.readFileSync(root+'/fixture.json','utf8'));
const base = process.env.CSX_ACCEPTANCE_BASE_URL || 'http://127.0.0.1:3000';
const evidence = {startedAt:new Date().toISOString(),roomId,fixture,build:JSON.parse(fs.readFileSync(root+'/build.json','utf8')),routes:[],consoleErrors:[],networkFailures:[],httpErrors:[],screenshots:[]};
const readinessStarted=performance.now();
let ready=false;
while(performance.now()-readinessStarted<30000){try{const r=await fetch(base+'/version');const value=await r.json();if(r.status===200&&value.service==='csx-server'&&(!evidence.build.commit||value.revision===evidence.build.commit)){evidence.servedVersion=value;ready=true;break;}}catch{}await new Promise(resolve=>setTimeout(resolve,250));}
assert.ok(ready,'candidate server readiness within 30s');evidence.readinessMs=Math.round(performance.now()-readinessStarted);
const browser = await chromium.launch({headless:true});
let failure;
try {
 for (const viewport of [{width:1440,height:1000},{width:390,height:844}]) {
  const context = await browser.newContext({viewport,locale:'en-US'});
  const page = await context.newPage();
  page.on('pageerror',err=>evidence.consoleErrors.push(String(err)));
  page.on('console',msg=>{if(msg.type()==='error')evidence.consoleErrors.push(msg.text());});
  page.on('requestfailed',req=>evidence.networkFailures.push({url:req.url(),failure:req.failure()}));
  page.on('response',res=>{if(res.status()>=400)evidence.httpErrors.push({url:res.url(),status:res.status()});});
  for (const route of ['/', '/samples', fixture.package,fixture.symbol,fixture.sample]) {
   const start=performance.now();
   const response=await page.goto(base+route,{waitUntil:'networkidle',timeout:30000});
   assert.equal(response.status(),200,route);
   const body=await page.locator('body').innerText();assert.ok(body.length>100,route+' rendered body');
   if(route.includes('axios'))assert.match(body,/axios/i);
   if(route===fixture.sample)assert.match(body,/postJSON/,'sample artifact code rendered');
   const dimensions=await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth}));
   assert.ok(dimensions.scroll<=dimensions.width+1,JSON.stringify({route,viewport,dimensions}));
   evidence.routes.push({route,viewport,status:response.status(),elapsedMs:Math.round(performance.now()-start),finalURL:page.url(),dimensions});
   const shot=`${root}/route-${evidence.routes.length}-${viewport.width}.png`;await page.screenshot({path:shot,fullPage:true});evidence.screenshots.push(shot);
  }
  await page.goto(base+'/samples',{waitUntil:'networkidle'});
  await page.locator('input[name=q]').fill('axios');
  await Promise.all([page.waitForURL(/q=axios/),page.locator('input[name=q]').press('Enter')]);
  await page.waitForLoadState('networkidle');assert.match(await page.locator('body').innerText(),/axios/i);
  evidence.routes.push({route:'sample search via form',viewport,status:200,finalURL:page.url()});
  await context.close();
 }
 const api=await browser.newContext();
 for(let round=0;round<5;round++) {
  for(const route of ['/healthz','/version','/v1/stats','/samples',fixture.package,fixture.symbol,fixture.sample]) {
   const start=performance.now();const response=await api.request.get(base+route);assert.equal(response.status(),200,`${round} ${route}`);
   evidence.routes.push({route,round,status:response.status(),elapsedMs:Math.round(performance.now()-start),kind:'http-smoke'});
  }
  await new Promise(resolve=>setTimeout(resolve,1100));
 }
 await api.close();
 assert.deepEqual(evidence.consoleErrors,[]);assert.deepEqual(evidence.networkFailures,[]);assert.deepEqual(evidence.httpErrors,[]);
 evidence.result='PASS';
} catch(err) { failure=err;evidence.result='FAIL';evidence.error=String(err.stack||err); }
finally {await browser.close();evidence.finishedAt=new Date().toISOString();fs.writeFileSync(root+'/result.json',JSON.stringify(evidence,null,2));console.log(JSON.stringify(evidence,null,2));}
if(failure)process.exitCode=1;
