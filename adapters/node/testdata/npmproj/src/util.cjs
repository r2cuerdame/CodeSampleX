const fr = require('follow-redirects');

fr.wrap({ http: true });

module.exports = fr;
