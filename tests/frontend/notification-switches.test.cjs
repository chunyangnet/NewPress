const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
test('notification preferences share accessible switches and preserve form contracts',()=>{
 const files=['internal/coreui','themes/chunnel'].map(root=>fs.readFileSync(`${root}/templates/partials/notification-settings.html`,'utf8'));
 assert.equal(files[0],files[1]);
 for(const name of ['reply','mention','publication','moderation','email','weekly']) {
  assert.ok(files[0].includes(`role="switch" name="${name}" aria-labelledby="notification-${name}-label"`));
  assert.ok(files[0].includes(`id="notification-${name}-label"`));
 }
 assert.equal((files[0].match(/<i aria-hidden="true"><\/i>/g)||[]).length,6);
 assert.equal((files[0].match(/<fieldset class="switch-stack" disabled>/g)||[]).length,2);
 for(const hook of ['data-notification-settings-form','data-notification-weekly-hint','data-notification-settings-discard','data-notification-settings-result'])assert.ok(files[0].includes(hook));
});
