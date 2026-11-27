import { shopServices } from '../shared/shop-services';
declare global { interface Window { NewPressShopServices: typeof shopServices } }
window.NewPressShopServices = shopServices;
