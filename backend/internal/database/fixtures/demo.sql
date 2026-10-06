INSERT INTO models(id,make,name) VALUES
 ('10000000-0000-4000-8000-000000000001','Toyota','Camry'),
 ('10000000-0000-4000-8000-000000000002','Hyundai','Tucson');
INSERT INTO trims(id,model_id,name) VALUES
 ('20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','Premium'),
 ('20000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000002','Comfort');
INSERT INTO cars(id,model_id,trim_id,vin,condition,year,mileage_km,price_minor,currency,color,fuel,transmission,description,publication) VALUES
 ('30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','JTDZZZ00000000001','new',2025,15,420000000,'RUB','white','petrol','automatic','Fictional demo vehicle. Replace with actual dealership inventory.','published'),
 ('30000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000002','20000000-0000-4000-8000-000000000002','KMHZZZ00000000002','used',2021,58000,245000000,'RUB','grey','petrol','automatic','Fictional used demo vehicle. History and condition require verification.','published');
INSERT INTO audit_log(action,entity_id) SELECT 'demo.car_created',id FROM cars;
